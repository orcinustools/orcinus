#include <stdio.h>
#include <time.h>
#include <stdlib.h>
int main(void) {
  setenv("TZ", "Asia/Jakarta", 1); tzset();
  time_t t = time(NULL); char b[64];
  strftime(b, sizeof b, "%Z", localtime(&t));
  printf("ok   glibc dynamic binary (TZ=%s)\n", b);
  return 0;
}
