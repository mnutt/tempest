/* Minimal x86_64 static binary for testing binfmt_misc/Rosetta.
 * Build: x86_64-linux-gnu-gcc -static -o hello-x86_64 hello-x86_64.c
 */
#include <unistd.h>

int main(void) {
    write(1, "Hello from x86_64!\n", 19);
    return 0;
}
