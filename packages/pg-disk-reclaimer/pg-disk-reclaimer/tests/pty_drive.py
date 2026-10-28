"""Run a command on a real pseudo-terminal and answer its prompts.

Usage:
  pty_drive.py [--answer TEXT | --eof] [--prompt REGEX] [--timeout SECONDS] -- CMD [ARG...]
  pty_drive.py --probe

The child runs as a session leader with the pty as its controlling terminal, so
`/dev/tty` inside it works exactly as it does in an interactive shell. The whole
transcript is printed to stdout, and the exit status is the child's.

--prompt REGEX is matched (re.MULTILINE, so `^` and `$` anchor a LINE) against
the transcript. Each new match is answered once: with TEXT plus a newline, or
with an end-of-file (Ctrl-D) under --eof. Matching only a whole line keeps text a
registry command prints (a "[y/N]" in the middle of a line) from being answered.
Without --prompt nothing is ever typed.

Exit 97 means the run exceeded --timeout (the child hung), which is the failure
this driver exists to catch: a prompt nobody can see. On a timeout the child's
whole process group is killed, not just the child.

--probe checks that this environment can allocate a pty AND open /dev/tty inside
it, the two things the prompt tests need. Exit 0 yes, 1 no.
"""

import argparse
import os
import pty
import re
import select
import signal
import sys
import time

EOF_BYTE = b"\x04"


def probe() -> int:
    try:
        pid, fd = pty.fork()
    except OSError:
        return 1
    if pid == 0:
        try:
            with open("/dev/tty", "w"):
                pass
        except OSError:
            os._exit(1)
        os._exit(0)
    try:
        # Drain so the child can never block on a full pty buffer.
        while os.read(fd, 4096):
            pass
    except OSError:
        pass
    _, status = os.waitpid(pid, 0)
    return 0 if os.waitstatus_to_exitcode(status) == 0 else 1


def kill_group(pid: int) -> None:
    """SIGKILL the child's process group (the child is its leader), then reap it."""
    try:
        os.killpg(pid, signal.SIGKILL)
    except OSError:
        pass
    try:
        os.waitpid(pid, 0)
    except OSError:
        pass


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--answer", default="y")
    parser.add_argument("--eof", action="store_true")
    parser.add_argument("--prompt", default=None)
    parser.add_argument("--timeout", type=float, default=20.0)
    parser.add_argument("--probe", action="store_true")
    parser.add_argument("cmd", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    if args.probe:
        return probe()
    cmd = args.cmd[1:] if args.cmd[:1] == ["--"] else args.cmd
    if not cmd:
        parser.error("no command given")
    prompt_re = re.compile(args.prompt, re.MULTILINE) if args.prompt else None

    pid, fd = pty.fork()
    if pid == 0:
        os.execvp(cmd[0], cmd)

    transcript = b""
    scan_from = 0
    deadline = time.monotonic() + args.timeout

    def timed_out() -> int:
        kill_group(pid)
        sys.stdout.write(transcript.decode(errors="replace"))
        sys.stdout.write("\n[pty_drive: timed out; the command hung]\n")
        return 97

    while True:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            return timed_out()
        ready, _, _ = select.select([fd], [], [], min(remaining, 0.2))
        if ready:
            try:
                chunk = os.read(fd, 4096)
            except OSError:
                break
            if not chunk:
                break
            transcript += chunk
        if prompt_re is not None:
            match = prompt_re.search(transcript.decode(errors="replace"), scan_from)
            if match:
                scan_from = match.end()
                os.write(fd, EOF_BYTE if args.eof else (args.answer + "\n").encode())

    # The pty is closed; the child may still be running (it closed its terminal
    # and carried on), so reaping it has a deadline too.
    while True:
        done, status = os.waitpid(pid, os.WNOHANG)
        if done:
            break
        if time.monotonic() >= deadline:
            return timed_out()
        time.sleep(0.05)
    sys.stdout.write(transcript.decode(errors="replace"))
    return os.waitstatus_to_exitcode(status)


if __name__ == "__main__":
    sys.exit(main())
