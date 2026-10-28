"""Run a command on a real pseudo-terminal and answer its prompts.

Usage: pty_drive.py [--answer TEXT] [--wait-for TEXT] [--timeout SECONDS] -- CMD [ARG...]

The child runs as a session leader with the pty as its controlling terminal, so
`/dev/tty` inside it works exactly as it does in an interactive shell. Every time
--wait-for TEXT newly appears in the output, TEXT-answer --answer plus a newline
is typed. The whole transcript is printed to stdout, and the exit status is the
child's. Exit 97 means the run exceeded --timeout (the child hung), which is the
failure this driver exists to catch: a prompt nobody can see.
"""

import argparse
import os
import pty
import select
import sys
import time


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--answer", default="y")
    parser.add_argument("--wait-for", default=None)
    parser.add_argument("--timeout", type=float, default=20.0)
    parser.add_argument("cmd", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    cmd = args.cmd[1:] if args.cmd[:1] == ["--"] else args.cmd
    if not cmd:
        parser.error("no command given")

    pid, fd = pty.fork()
    if pid == 0:
        os.execvp(cmd[0], cmd)

    transcript = b""
    answered = 0
    deadline = time.monotonic() + args.timeout
    while True:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            os.kill(pid, 9)
            os.waitpid(pid, 0)
            sys.stdout.write(transcript.decode(errors="replace"))
            sys.stdout.write("\n[pty_drive: timed out; the command hung]\n")
            return 97
        ready, _, _ = select.select([fd], [], [], min(remaining, 0.2))
        if ready:
            try:
                chunk = os.read(fd, 4096)
            except OSError:
                break
            if not chunk:
                break
            transcript += chunk
        if args.wait_for is not None:
            seen = transcript.decode(errors="replace").count(args.wait_for)
            while answered < seen:
                os.write(fd, (args.answer + "\n").encode())
                answered += 1

    _, status = os.waitpid(pid, 0)
    sys.stdout.write(transcript.decode(errors="replace"))
    return os.waitstatus_to_exitcode(status)


if __name__ == "__main__":
    sys.exit(main())
