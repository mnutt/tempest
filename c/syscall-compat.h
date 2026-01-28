// syscall-compat.h - ARM64 compatibility for legacy x86_64 syscalls
//
// ARM64 doesn't define many legacy syscalls that exist on x86_64.
// Define them as 0xDEAD so they won't match any real syscall number
// (since we compare unsigned values, this becomes a very large number
// that will never match an actual syscall).
//
// This allows the same filter.s to compile on both architectures.

#ifndef SYSCALL_COMPAT_H
#define SYSCALL_COMPAT_H

#ifndef SYS_access
#define SYS_access 0xDEAD
#endif
#ifndef SYS_alarm
#define SYS_alarm 0xDEAD
#endif
#ifndef SYS_chmod
#define SYS_chmod 0xDEAD
#endif
#ifndef SYS_creat
#define SYS_creat 0xDEAD
#endif
#ifndef SYS_dup2
#define SYS_dup2 0xDEAD
#endif
#ifndef SYS_epoll_create
#define SYS_epoll_create 0xDEAD
#endif
#ifndef SYS_epoll_wait
#define SYS_epoll_wait 0xDEAD
#endif
#ifndef SYS_eventfd
#define SYS_eventfd 0xDEAD
#endif
#ifndef SYS_fork
#define SYS_fork 0xDEAD
#endif
#ifndef SYS_fstat
#define SYS_fstat 0xDEAD
#endif
#ifndef SYS_getdents
#define SYS_getdents 0xDEAD
#endif
#ifndef SYS_getpgrp
#define SYS_getpgrp 0xDEAD
#endif
#ifndef SYS_getrlimit
#define SYS_getrlimit 0xDEAD
#endif
#ifndef SYS_inotify_init
#define SYS_inotify_init 0xDEAD
#endif
#ifndef SYS_link
#define SYS_link 0xDEAD
#endif
#ifndef SYS_lstat
#define SYS_lstat 0xDEAD
#endif
#ifndef SYS_mkdir
#define SYS_mkdir 0xDEAD
#endif
#ifndef SYS_open
#define SYS_open 0xDEAD
#endif
#ifndef SYS_pause
#define SYS_pause 0xDEAD
#endif
#ifndef SYS_pipe
#define SYS_pipe 0xDEAD
#endif
#ifndef SYS_poll
#define SYS_poll 0xDEAD
#endif
#ifndef SYS_readlink
#define SYS_readlink 0xDEAD
#endif
#ifndef SYS_rename
#define SYS_rename 0xDEAD
#endif
#ifndef SYS_rmdir
#define SYS_rmdir 0xDEAD
#endif
#ifndef SYS_select
#define SYS_select 0xDEAD
#endif
#ifndef SYS_stat
#define SYS_stat 0xDEAD
#endif
#ifndef SYS_symlink
#define SYS_symlink 0xDEAD
#endif
#ifndef SYS_unlink
#define SYS_unlink 0xDEAD
#endif
#ifndef SYS_utime
#define SYS_utime 0xDEAD
#endif
#ifndef SYS_utimes
#define SYS_utimes 0xDEAD
#endif
#ifndef SYS_vfork
#define SYS_vfork 0xDEAD
#endif
#ifndef SYS_time
#define SYS_time 0xDEAD
#endif
#ifndef SYS_signalfd
#define SYS_signalfd 0xDEAD
#endif
#ifndef SYS_arch_prctl
#define SYS_arch_prctl 0xDEAD
#endif
#ifndef SYS_chown
#define SYS_chown 0xDEAD
#endif
#ifndef SYS_lchown
#define SYS_lchown 0xDEAD
#endif

#endif // SYSCALL_COMPAT_H
