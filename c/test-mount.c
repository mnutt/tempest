/* Minimal test program to replicate sandbox-launcher mount behavior.
 * This tests if the bind mount from /tmp/lima-rosetta works with the
 * same mount operations as sandbox-launcher.c
 *
 * Build: cc -static -o test-mount test-mount.c
 * Run inside VM: ./test-mount <package-id>
 */
#define _GNU_SOURCE

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <errno.h>
#include <sys/mount.h>
#include <sys/stat.h>
#include <sys/syscall.h>
#include <sys/wait.h>
#include <sys/prctl.h>
#include <sched.h>
#include <unistd.h>
#include <fcntl.h>
#include <linux/filter.h>
#include <linux/seccomp.h>

/* Include the compiled seccomp filter */
static struct sock_filter seccomp_filter[] = {
#include "bpf_filter.h"
};

static struct sock_fprog seccomp_fprog = (struct sock_fprog) {
	.len = sizeof seccomp_filter / sizeof seccomp_filter[0],
	.filter = &seccomp_filter[0],
};

#define SANDSTORM_STATE   "/sandstorm"
#define IMAGE_DIR     SANDSTORM_STATE   "/apps"
#define SANDBOX_DIR   SANDSTORM_STATE   "/grains"
#define CHROOT_MNT    SANDSTORM_STATE   "/mnt"

int main(int argc, char **argv) {
    fprintf(stderr, "=== C Mount Test (replicating sandbox-launcher) ===\n");
    fflush(stderr);

    if (argc < 2) {
        fprintf(stderr, "Usage: %s <package-id> [grain-id]\n", argv[0]);
        return 1;
    }

    const char *image_id = argv[1];
    const char *sandbox_id = argc > 2 ? argv[2] : NULL;

    fprintf(stderr, "Package: %s\n", image_id);
    fprintf(stderr, "Grain: %s\n", sandbox_id ? sandbox_id : "(none)");
    fflush(stderr);

    /* Step 1: Unshare namespaces (like sandbox-launcher.c lines 178-187) */
    fprintf(stderr, "\n[1] Unsharing namespaces...\n");
    fflush(stderr);
    if (unshare(
        CLONE_NEWNS |
        CLONE_FILES |
        CLONE_FS |
        CLONE_NEWCGROUP |
        CLONE_NEWIPC |
        CLONE_NEWNET |
        CLONE_NEWUTS |
        CLONE_SYSVSEM) != 0) {
        fprintf(stderr, "    FAIL: unshare failed: %s\n", strerror(errno));
        return 1;
    }
    fprintf(stderr, "    OK: unshare succeeded\n");
    fflush(stderr);

    /* Step 2: Make mounts slave (like sandbox-launcher.c line 193) */
    fprintf(stderr, "\n[2] Making mounts slave...\n");
    fflush(stderr);
    if (mount("", "/", "", MS_REC|MS_SLAVE, "") != 0) {
        fprintf(stderr, "    FAIL: mount --make-rslave failed: %s\n", strerror(errno));
        return 1;
    }
    fprintf(stderr, "    OK: mount --make-rslave succeeded\n");
    fflush(stderr);

    /* Step 3: Mount package image (like sandbox-launcher.c lines 219-222) */
    fprintf(stderr, "\n[3] Mounting package image...\n");
    fflush(stderr);
    if (chdir(IMAGE_DIR) != 0) {
        fprintf(stderr, "    FAIL: chdir to %s failed: %s\n", IMAGE_DIR, strerror(errno));
        return 1;
    }
    if (mount(image_id, CHROOT_MNT, "", MS_BIND, "") != 0) {
        fprintf(stderr, "    FAIL: mount --bind %s %s failed: %s\n", image_id, CHROOT_MNT, strerror(errno));
        return 1;
    }
    fprintf(stderr, "    OK: mounted package at %s\n", CHROOT_MNT);
    fflush(stderr);

    if (mount("", CHROOT_MNT, "", MS_REMOUNT|MS_BIND|MS_RDONLY, "") != 0) {
        fprintf(stderr, "    WARN: remount ro failed: %s\n", strerror(errno));
    }

    /* Step 4: Mount grain sandbox (like sandbox-launcher.c lines 223-225) */
    if (sandbox_id) {
        fprintf(stderr, "\n[4] Mounting grain sandbox...\n");
        fflush(stderr);
        if (chdir(SANDBOX_DIR) != 0) {
            fprintf(stderr, "    FAIL: chdir to %s failed: %s\n", SANDBOX_DIR, strerror(errno));
            return 1;
        }
        if (chdir(sandbox_id) != 0) {
            fprintf(stderr, "    FAIL: chdir to %s failed: %s\n", sandbox_id, strerror(errno));
            return 1;
        }
        char cwd[256];
        if (getcwd(cwd, sizeof(cwd))) {
            fprintf(stderr, "    Current directory: %s\n", cwd);
        }
        if (mount("sandbox", CHROOT_MNT "/var", "", MS_BIND, "") != 0) {
            fprintf(stderr, "    WARN: mount sandbox failed: %s\n", strerror(errno));
        } else {
            fprintf(stderr, "    OK: mounted sandbox at %s/var\n", CHROOT_MNT);
        }
        fflush(stderr);
    }

    /* Step 5: Mount /tmp as tmpfs (like sandbox-launcher.c line 234) */
    fprintf(stderr, "\n[5] Mounting tmpfs at /tmp...\n");
    fflush(stderr);
    if (mount("none", CHROOT_MNT "/tmp", "tmpfs", MS_NODEV|MS_NOSUID, "size=16m") != 0) {
        fprintf(stderr, "    FAIL: mount tmpfs at %s/tmp failed: %s\n", CHROOT_MNT, strerror(errno));
        return 1;
    }
    fprintf(stderr, "    OK: mounted tmpfs at %s/tmp\n", CHROOT_MNT);
    fflush(stderr);

    /* Step 6: Mount proc (like sandbox-launcher.c line 231) */
    fprintf(stderr, "\n[6] Mounting proc...\n");
    fflush(stderr);
    if (mount("proc", CHROOT_MNT "/proc", "proc", MS_NOSUID|MS_NODEV|MS_NOEXEC, "") != 0) {
        fprintf(stderr, "    WARN: mount proc failed: %s\n", strerror(errno));
    } else {
        fprintf(stderr, "    OK: mounted proc at %s/proc\n", CHROOT_MNT);
    }

    /* Step 7: Mount Rosetta (like sandbox-launcher.c lines 240-272) */
    fprintf(stderr, "\n[7] Mounting Rosetta...\n");
    fflush(stderr);

    /* Check if /tmp/lima-rosetta exists */
    struct stat st;
    if (stat("/tmp/lima-rosetta", &st) == 0) {
        fprintf(stderr, "    /tmp/lima-rosetta exists (mode=%o)\n", st.st_mode);
    } else {
        fprintf(stderr, "    ERROR: /tmp/lima-rosetta does NOT exist: %s\n", strerror(errno));
        return 1;
    }

    /* Create mount point */
    if (mkdir(CHROOT_MNT "/tmp/lima-rosetta", 0755) != 0) {
        fprintf(stderr, "    FAIL: mkdir %s/tmp/lima-rosetta failed: %s\n", CHROOT_MNT, strerror(errno));
        return 1;
    }
    fprintf(stderr, "    Created mount point at %s/tmp/lima-rosetta\n", CHROOT_MNT);

    /* Try bind mount */
    fprintf(stderr, "    Attempting bind mount: /tmp/lima-rosetta -> %s/tmp/lima-rosetta\n", CHROOT_MNT);
    fflush(stderr);
    if (mount("/tmp/lima-rosetta", CHROOT_MNT "/tmp/lima-rosetta", "", MS_BIND, "") != 0) {
        fprintf(stderr, "    BIND MOUNT FAILED: %s (errno=%d)\n", strerror(errno), errno);
        fprintf(stderr, "    Trying virtiofs fallback...\n");
        fflush(stderr);
        if (mount("rosetta", CHROOT_MNT "/tmp/lima-rosetta", "virtiofs", 0, "") != 0) {
            fprintf(stderr, "    VIRTIOFS FAILED: %s (errno=%d)\n", strerror(errno), errno);
            return 1;
        }
        fprintf(stderr, "    Mounted via virtiofs (ioctl will likely fail)\n");
    } else {
        fprintf(stderr, "    BIND MOUNT SUCCEEDED!\n");
    }
    fflush(stderr);

    /* Test Rosetta */
    fprintf(stderr, "\n[8] Testing Rosetta...\n");
    fflush(stderr);

    char rosetta_path[256];
    snprintf(rosetta_path, sizeof(rosetta_path), "%s/tmp/lima-rosetta/rosetta", CHROOT_MNT);
    if (stat(rosetta_path, &st) != 0) {
        fprintf(stderr, "    FAIL: rosetta binary not found at %s: %s\n", rosetta_path, strerror(errno));
        return 1;
    }
    fprintf(stderr, "    Rosetta binary found at %s\n", rosetta_path);

    /* Show mount info */
    fprintf(stderr, "\n[9] Mount info (rosetta):\n");
    fflush(stderr);
    FILE *f = fopen("/proc/self/mountinfo", "r");
    if (f) {
        char line[512];
        while (fgets(line, sizeof(line), f)) {
            if (strstr(line, "rosetta") || strstr(line, "lima")) {
                fprintf(stderr, "    %s", line);
            }
        }
        fclose(f);
    }

    /* Step 9: Set up /dev (like sandbox-launcher.c lines 274-280) */
    fprintf(stderr, "\n[10] Setting up /dev...\n");
    fflush(stderr);
    if (mount("none", CHROOT_MNT "/dev", "tmpfs", MS_NOSUID, "") != 0) {
        fprintf(stderr, "    WARN: mount tmpfs at /dev failed: %s\n", strerror(errno));
    }

    /* Step 9b: Copy hello-x86_64 to /tmp for binfmt_misc testing */
    fprintf(stderr, "\n[10b] Copying hello-x86_64 to sandbox...\n");
    fflush(stderr);
    {
        int src = open("/bin/hello-x86_64", O_RDONLY);
        if (src >= 0) {
            int dst = open(CHROOT_MNT "/tmp/hello-x86_64", O_WRONLY|O_CREAT|O_TRUNC, 0755);
            if (dst >= 0) {
                char buf[4096];
                ssize_t n;
                while ((n = read(src, buf, sizeof(buf))) > 0) {
                    write(dst, buf, n);
                }
                close(dst);
                fprintf(stderr, "    Copied hello-x86_64 to %s/tmp/hello-x86_64\n", CHROOT_MNT);
            } else {
                fprintf(stderr, "    Could not create destination: %s\n", strerror(errno));
            }
            close(src);
        } else {
            fprintf(stderr, "    hello-x86_64 not found at /bin/hello-x86_64\n");
        }
    }
    fflush(stderr);

    /* Step 10c: Run strace on rosetta BEFORE pivot_root (using chroot) to capture ioctls */
    fprintf(stderr, "\n[10c] Running strace on Rosetta (before pivot_root) to capture ioctls...\n");
    fflush(stderr);
    if (stat("/bin/strace", &st) == 0) {
        /* Copy strace to sandbox */
        int src = open("/bin/strace", O_RDONLY);
        if (src >= 0) {
            int dst = open(CHROOT_MNT "/tmp/strace", O_WRONLY|O_CREAT|O_TRUNC, 0755);
            if (dst >= 0) {
                char buf[4096];
                ssize_t n;
                while ((n = read(src, buf, sizeof(buf))) > 0) {
                    write(dst, buf, n);
                }
                close(dst);
            }
            close(src);
        }

        pid_t strace_pid = fork();
        if (strace_pid == 0) {
            /* Child: chroot and run strace */
            if (chroot(CHROOT_MNT) != 0) {
                fprintf(stderr, "    chroot failed: %s\n", strerror(errno));
                _exit(1);
            }
            chdir("/");
            /* Run: strace -e ioctl /tmp/lima-rosetta/rosetta */
            char *argv[] = {"/tmp/strace", "-e", "ioctl", "/tmp/lima-rosetta/rosetta", NULL};
            char *envp[] = {NULL};
            execve("/tmp/strace", argv, envp);
            fprintf(stderr, "    execve strace failed: %s\n", strerror(errno));
            _exit(1);
        } else if (strace_pid > 0) {
            int status;
            waitpid(strace_pid, &status, 0);
            fprintf(stderr, "    strace completed (exit=%d)\n",
                WIFEXITED(status) ? WEXITSTATUS(status) : -1);
        }
    } else {
        fprintf(stderr, "    strace not available at /bin/strace\n");
    }
    fflush(stderr);

    /* Step 10: pivot_root (EXACTLY like sandbox-launcher.c lines 336-344) */
    fprintf(stderr, "\n[11] Performing pivot_root (sandbox-launcher style)...\n");
    fflush(stderr);

    /* Make sure we're inside the sandbox's root. */
    if (chdir(CHROOT_MNT) != 0) {
        fprintf(stderr, "    FAIL: chdir to chroot failed: %s\n", strerror(errno));
        return 1;
    }

    /* Open fd to old root before pivot_root */
    int old_root = open("/", O_RDONLY | O_DIRECTORY | O_CLOEXEC);
    if (old_root < 0) {
        fprintf(stderr, "    FAIL: open / failed: %s\n", strerror(errno));
        return 1;
    }

    /* Use the "weird" pivot_root from sandbox-launcher:
     * CHROOT_MNT as both new_root and put_old.
     * This mounts old root ON TOP of the new root. */
    if (syscall(SYS_pivot_root, CHROOT_MNT, CHROOT_MNT) != 0) {
        fprintf(stderr, "    FAIL: pivot_root failed: %s\n", strerror(errno));
        return 1;
    }
    fprintf(stderr, "    pivot_root succeeded (old root mounted on top)\n");
    fflush(stderr);

    /* Unmount the old root using the fd we saved */
    if (fchdir(old_root) != 0) {
        fprintf(stderr, "    WARN: fchdir to old_root failed: %s\n", strerror(errno));
    }
    if (umount2(".", MNT_DETACH) != 0) {
        fprintf(stderr, "    WARN: umount old root failed: %s\n", strerror(errno));
    }
    close(old_root);

    /* Now actually move into the new root */
    if (chdir("/") != 0) {
        fprintf(stderr, "    WARN: chdir to / failed: %s\n", strerror(errno));
    }

    fprintf(stderr, "    Now inside new root\n");
    fflush(stderr);

    /* Use strace to capture ALL ioctls Rosetta uses when running x86_64 binary */
    fprintf(stderr, "\n[12a] Running strace on hello-x86_64 to capture ALL Rosetta ioctls...\n");
    fflush(stderr);
    {
        /* Check if strace and hello-x86_64 are available */
        if (stat("/tmp/strace", &st) == 0 && stat("/tmp/hello-x86_64", &st) == 0) {
            pid_t pre_pid = fork();
            if (pre_pid == 0) {
                /* Run: strace -f -e ioctl /tmp/hello-x86_64
                 * -f follows forks to capture child process ioctls */
                char *argv[] = {"/tmp/strace", "-f", "-e", "ioctl", "/tmp/hello-x86_64", NULL};
                char *envp[] = {NULL};
                execve("/tmp/strace", argv, envp);
                _exit(1);
            } else if (pre_pid > 0) {
                int status;
                waitpid(pre_pid, &status, 0);
                fprintf(stderr, "    strace on hello-x86_64 completed (exit=%d)\n",
                    WIFEXITED(status) ? WEXITSTATUS(status) : -1);
            }
        } else {
            fprintf(stderr, "    strace or hello-x86_64 not available\n");
        }
    }
    fflush(stderr);

    /* Step 11a: Set PR_SET_NO_NEW_PRIVS (required for unprivileged seccomp) */
    fprintf(stderr, "\n[12b] Installing seccomp filter...\n");
    fflush(stderr);
    if (prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) != 0) {
        fprintf(stderr, "    FAIL: PR_SET_NO_NEW_PRIVS failed: %s\n", strerror(errno));
        return 1;
    }
    fprintf(stderr, "    PR_SET_NO_NEW_PRIVS set\n");

    /* Step 11b: Install seccomp filter */
    if (syscall(SYS_seccomp, SECCOMP_SET_MODE_FILTER, 0, &seccomp_fprog) != 0) {
        fprintf(stderr, "    FAIL: seccomp filter install failed: %s\n", strerror(errno));
        return 1;
    }
    fprintf(stderr, "    Seccomp filter installed\n");
    fflush(stderr);

    /* Step 12: Test Rosetta AFTER pivot_root */
    fprintf(stderr, "\n[13] Testing Rosetta after pivot_root + seccomp...\n");
    fflush(stderr);

    if (stat("/tmp/lima-rosetta/rosetta", &st) != 0) {
        fprintf(stderr, "    FAIL: rosetta not found at /tmp/lima-rosetta/rosetta: %s\n", strerror(errno));
        return 1;
    }
    fprintf(stderr, "    Rosetta binary exists at /tmp/lima-rosetta/rosetta\n");

    /* Actually run rosetta to test ioctl */
    fprintf(stderr, "    Running rosetta to test ioctl...\n");
    fflush(stderr);

    int pid = fork();
    if (pid == 0) {
        /* Child: exec rosetta */
        char *argv[] = {"/tmp/lima-rosetta/rosetta", NULL};
        char *envp[] = {NULL};
        execve("/tmp/lima-rosetta/rosetta", argv, envp);
        fprintf(stderr, "    execve failed: %s\n", strerror(errno));
        _exit(1);
    } else if (pid > 0) {
        int status;
        waitpid(pid, &status, 0);
        if (WIFEXITED(status)) {
            fprintf(stderr, "    rosetta exited with code %d\n", WEXITSTATUS(status));
        }
    }

    /* Test hello-x86_64 via binfmt_misc */
    fprintf(stderr, "\n[14] Testing hello-x86_64 via binfmt_misc...\n");
    fflush(stderr);

    if (stat("/tmp/hello-x86_64", &st) == 0) {
        fprintf(stderr, "    Found hello-x86_64 at /tmp/hello-x86_64\n");
        pid = fork();
        if (pid == 0) {
            char *argv[] = {"/tmp/hello-x86_64", NULL};
            char *envp[] = {NULL};
            execve("/tmp/hello-x86_64", argv, envp);
            fprintf(stderr, "    execve failed: %s\n", strerror(errno));
            _exit(1);
        } else if (pid > 0) {
            int status;
            waitpid(pid, &status, 0);
            if (WIFEXITED(status) && WEXITSTATUS(status) == 0) {
                fprintf(stderr, "    *** SUCCESS: hello-x86_64 works via binfmt_misc! ***\n");
            } else {
                fprintf(stderr, "    *** FAIL: hello-x86_64 failed (exit=%d) ***\n",
                    WIFEXITED(status) ? WEXITSTATUS(status) : -1);
            }
        }
    } else {
        fprintf(stderr, "    hello-x86_64 not found, skipping binfmt_misc test\n");
    }

    /* Show mount info after pivot_root */
    fprintf(stderr, "\n[15] Mount info after pivot_root (rosetta):\n");
    fflush(stderr);
    f = fopen("/proc/self/mountinfo", "r");
    if (f) {
        char line[512];
        while (fgets(line, sizeof(line), f)) {
            if (strstr(line, "rosetta") || strstr(line, "lima")) {
                fprintf(stderr, "    %s", line);
            }
        }
        fclose(f);
    }

    /* Show dmesg for seccomp logs */
    fprintf(stderr, "\n[16] Checking dmesg for seccomp logs...\n");
    fflush(stderr);

    /* Try to read dmesg via /dev/kmsg (ring buffer access) */
    int kmsg_fd = open("/dev/kmsg", O_RDONLY | O_NONBLOCK);
    if (kmsg_fd >= 0) {
        char buf[4096];
        ssize_t n;
        int found_seccomp = 0;
        /* Seek to end first, then back up a bit to get recent messages */
        lseek(kmsg_fd, 0, SEEK_END);
        /* Read any available messages */
        while ((n = read(kmsg_fd, buf, sizeof(buf)-1)) > 0) {
            buf[n] = '\0';
            if (strstr(buf, "seccomp") || strstr(buf, "ioctl")) {
                fprintf(stderr, "    DMESG: %s", buf);
                found_seccomp = 1;
            }
        }
        close(kmsg_fd);
        if (!found_seccomp) {
            fprintf(stderr, "    (no seccomp messages found in recent dmesg)\n");
        }
    } else {
        fprintf(stderr, "    (cannot read /dev/kmsg: %s)\n", strerror(errno));
    }

    fprintf(stderr, "\n=== Test Complete ===\n");
    return 0;
}
