/* Rosetta compatibility for x86_64 binary translation on Apple Silicon.
 *
 * This header provides functions to set up /proc and mount Rosetta
 * in a way that allows x86_64 binaries to run while minimizing
 * information exposure.
 *
 * Rosetta requires:
 * - /proc/self/exe (to locate the binary being translated)
 * - /proc/sys/vm/mmap_min_addr (for VMAllocationTracker)
 *
 * Everything else in /proc is masked for security.
 */

#ifndef ROSETTA_H
#define ROSETTA_H

#ifdef TEMPEST_ROSETTA_COMPAT

#include <dirent.h>
#include <fcntl.h>
#include <sys/mount.h>
#include <sys/stat.h>
#include <string.h>
#include <unistd.h>
#include <ctype.h>

/* Allowlist of /proc entries. Everything else gets masked.
 * "self" is handled specially - we allowlist specific entries under it. */
static const char *proc_allowlist[] = {
	"self",         /* Handled specially below */
	"sys",          /* Handled specially - we create only mmap_min_addr */
	NULL
};

/* Allowlist for /proc/self/ entries */
static const char *proc_self_allowlist[] = {
	"exe",          /* Required by Rosetta */
	NULL
};

static int in_allowlist(const char *name, const char **allowlist) {
	for (int i = 0; allowlist[i] != NULL; i++) {
		if (strcmp(name, allowlist[i]) == 0) {
			return 1;
		}
	}
	return 0;
}

static int is_pid_dir(const char *name) {
	/* Check if name is all digits (a PID directory) */
	if (*name == '\0') return 0;
	while (*name) {
		if (!isdigit(*name)) return 0;
		name++;
	}
	return 1;
}

/* Mask a /proc entry by bind-mounting /dev/null (files) or empty tmpfs (dirs) */
static void mask_proc_entry(const char *base_path, const char *name) {
	char path[512];
	struct stat st;

	snprintf(path, sizeof(path), "%s/%s", base_path, name);

	if (stat(path, &st) != 0) return;

	if (S_ISDIR(st.st_mode)) {
		mount("tmpfs", path, "tmpfs", MS_RDONLY|MS_NOSUID|MS_NODEV|MS_NOEXEC, "size=0");
	} else {
		mount("/dev/null", path, "", MS_BIND, "");
	}
}

/* Replace /proc/sys with tmpfs containing only mmap_min_addr */
static void setup_proc_sys(const char *proc_path) {
	char sys_path[512];
	char mmap_min_addr_value[32];
	int fd;

	/* Read the real value from the VM's /proc */
	fd = open("/proc/sys/vm/mmap_min_addr", O_RDONLY);
	if (fd < 0) return;

	ssize_t n = read(fd, mmap_min_addr_value, sizeof(mmap_min_addr_value) - 1);
	close(fd);
	if (n <= 0) return;
	mmap_min_addr_value[n] = '\0';

	/* Mount tmpfs over /proc/sys to hide everything */
	snprintf(sys_path, sizeof(sys_path), "%s/sys", proc_path);
	if (mount("tmpfs", sys_path, "tmpfs",
	          MS_NOSUID|MS_NODEV|MS_NOEXEC, "size=4k,mode=0555") != 0) {
		return;
	}

	/* Create only /proc/sys/vm/mmap_min_addr */
	char vm_path[512];
	snprintf(vm_path, sizeof(vm_path), "%s/sys/vm", proc_path);
	mkdir(vm_path, 0555);

	char mmap_path[512];
	snprintf(mmap_path, sizeof(mmap_path), "%s/sys/vm/mmap_min_addr", proc_path);
	fd = open(mmap_path, O_WRONLY|O_CREAT, 0444);
	if (fd >= 0) {
		write(fd, mmap_min_addr_value, strlen(mmap_min_addr_value));
		close(fd);
	}
}

/* Apply allowlist to /proc - mask everything not in allowlist */
static void apply_proc_allowlist(const char *proc_path) {
	DIR *dir;
	struct dirent *entry;
	char self_path[512];

	/* First pass: mask top-level /proc entries not in allowlist */
	dir = opendir(proc_path);
	if (!dir) return;

	while ((entry = readdir(dir)) != NULL) {
		if (strcmp(entry->d_name, ".") == 0 || strcmp(entry->d_name, "..") == 0)
			continue;

		/* Skip PID directories (hidepid=2 handles these) */
		if (is_pid_dir(entry->d_name))
			continue;

		/* Skip if in allowlist */
		if (in_allowlist(entry->d_name, proc_allowlist))
			continue;

		/* Mask this entry */
		mask_proc_entry(proc_path, entry->d_name);
	}
	closedir(dir);

	/* Handle /proc/sys specially - replace with tmpfs containing only mmap_min_addr */
	setup_proc_sys(proc_path);

	/* Second pass: mask /proc/self entries not in allowlist */
	snprintf(self_path, sizeof(self_path), "%s/self", proc_path);
	dir = opendir(self_path);
	if (!dir) return;

	while ((entry = readdir(dir)) != NULL) {
		if (strcmp(entry->d_name, ".") == 0 || strcmp(entry->d_name, "..") == 0)
			continue;

		if (in_allowlist(entry->d_name, proc_self_allowlist))
			continue;

		mask_proc_entry(self_path, entry->d_name);
	}
	closedir(dir);
}

/* Mount procfs with minimal exposure for Rosetta compatibility.
 * Returns 0 on success, -1 on failure. */
static int rosetta_setup_proc(const char *chroot_mnt) {
	char proc_path[512];
	snprintf(proc_path, sizeof(proc_path), "%s/proc", chroot_mnt);

	/* Mount procfs with hidepid=2 so processes can only see their own entries */
	if (mount("proc", proc_path, "proc",
	          MS_NOSUID|MS_NODEV|MS_NOEXEC, "hidepid=2") != 0) {
		return -1;
	}

	/* Apply the allowlist - mask everything not explicitly allowed */
	apply_proc_allowlist(proc_path);
	return 0;
}

/* Mount Rosetta for x86_64 binary translation.
 * Try bind mount from VM's existing mount first - this preserves the virtiofs ioctl context.
 * Fall back to direct virtiofs mount if bind mount fails.
 * Apply restrictive flags for defense in depth. */
static void rosetta_mount(const char *chroot_mnt) {
	char rosetta_path[512];
	snprintf(rosetta_path, sizeof(rosetta_path), "%s/tmp/rosetta", chroot_mnt);

	if (mkdir(rosetta_path, 0755) != 0) {
		return;
	}

	int mounted = 0;
	/* Try bind mount from existing VM mount first */
	if (mount("/tmp/rosetta", rosetta_path, "", MS_BIND, "") == 0) {
		mounted = 1;
	} else if (mount("rosetta", rosetta_path, "virtiofs", MS_NOSUID|MS_NODEV, "") == 0) {
		/* Fall back to direct virtiofs mount */
		mounted = 1;
	}

	/* Apply restrictive flags via remount (bind mounts require this) */
	if (mounted) {
		mount("", rosetta_path, "", MS_REMOUNT|MS_BIND|MS_RDONLY|MS_NOSUID|MS_NODEV, "");
	}
}

#endif /* TEMPEST_ROSETTA_COMPAT */
#endif /* ROSETTA_H */
