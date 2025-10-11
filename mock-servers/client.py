#!/usr/bin/env python3
import argparse
import cmd
import json
import os
import shlex
import subprocess
import sys
from collections import deque
from pathlib import Path

import readline  # noqa: F401  (ensures command history support where available)
import requests

DEFAULT_BASE_PORT = 10000
DEFAULT_MAX_PORT = 60000
DEFAULT_MGMT_PORT = 9999


class MockServerShell(cmd.Cmd):
    intro = 'Welcome to the mock server shell. Type help or ? to list commands.'
    prompt = '(mock-server) '

    def __init__(self):
        super().__init__()
        self.base_dir = Path(__file__).resolve().parent
        self.start_script = self.base_dir / "start_servers.sh"
        self.stop_script = self.base_dir / "cleanup.sh"
        self.state_file = self.base_dir / ".mock-servers-state.json"
        self.pid_file = self.base_dir / ".mock-servers.pid"

    # ---- Helpers -----------------------------------------------------------------
    def _split(self, arg: str):
        if not arg:
            return []
        try:
            return shlex.split(arg)
        except ValueError as exc:
            print(f"Error parsing arguments: {exc}", file=sys.stderr)
            return []

    def _run_subprocess(self, cmd_args):
        try:
            subprocess.run(cmd_args, cwd=self.base_dir, check=True)
        except FileNotFoundError:
            print(f"Error: command not found: {cmd_args[0]}", file=sys.stderr)
        except subprocess.CalledProcessError as exc:
            print(f"Command failed with exit code {exc.returncode}", file=sys.stderr)

    def _load_state(self):
        if not self.state_file.exists():
            return None
        try:
            with self.state_file.open("r", encoding="utf-8") as fh:
                return json.load(fh)
        except (json.JSONDecodeError, OSError) as exc:
            print(f"Warning: failed to read state file: {exc}", file=sys.stderr)
            return None

    @staticmethod
    def _pid_alive(pid: int) -> bool:
        try:
            os.kill(pid, 0)
        except OSError:
            return False
        return True

    def _running_state(self):
        state = self._load_state()
        pid = None

        if state and isinstance(state.get("pid"), int):
            pid = state["pid"]
        elif self.pid_file.exists():
            try:
                pid = int(self.pid_file.read_text().strip())
            except ValueError:
                pid = None

        if pid is not None and not self._pid_alive(pid):
            pid = None

        return state, pid

    def _get_mgmt_url(self, state=None):
        state = state or self._load_state()
        port = DEFAULT_MGMT_PORT
        if state:
            port = state.get("mgmt_port", port)
        return f"http://localhost:{port}", port

    def _tail_file(self, path: Path, lines: int):
        try:
            with path.open("r", encoding="utf-8", errors="replace") as fh:
                if lines <= 0:
                    print(fh.read(), end="")
                    return
                buffer = deque(fh, maxlen=lines)
        except FileNotFoundError:
            print(f"Error: log file not found at {path}", file=sys.stderr)
            return
        except OSError as exc:
            print(f"Error reading log file: {exc}", file=sys.stderr)
            return

        for line in buffer:
            print(line, end="")

    # ---- Commands ----------------------------------------------------------------
    def do_start(self, arg):
        """Start the mock servers.
        Usage: start <num_servers> [--base-port <port>] [--max-port <port>] [--rebuild]"""
        parser = argparse.ArgumentParser(prog='start')
        parser.add_argument('num_servers', type=int, help='Number of servers to start')
        parser.add_argument('--base-port', type=int, default=DEFAULT_BASE_PORT, help='Base port (default: 10000)')
        parser.add_argument('--max-port', type=int, default=DEFAULT_MAX_PORT, help='Maximum port (default: 60000)')
        parser.add_argument('--rebuild', action='store_true', help='Rebuild the Go binary before starting')

        tokens = self._split(arg)
        try:
            args = parser.parse_args(tokens)
        except SystemExit:
            return

        if not self.start_script.exists():
            print(f"Error: start script not found at {self.start_script}", file=sys.stderr)
            return

        cmd_args = [
            str(self.start_script),
            str(args.num_servers),
            "--base-port",
            str(args.base_port),
            "--max-port",
            str(args.max_port),
        ]
        if args.rebuild:
            cmd_args.append("--rebuild")

        self._run_subprocess(cmd_args)

    def do_stop_all(self, arg):
        """Stop the running mock server process."""
        if not self.stop_script.exists():
            print(f"Error: cleanup script not found at {self.stop_script}", file=sys.stderr)
            return
        self._run_subprocess([str(self.stop_script)])

    def do_endpoints(self, arg):
        """Get server endpoints from the management API.
        Usage: endpoints [limit]"""
        tokens = self._split(arg)
        limit = 1000
        if tokens:
            try:
                limit = int(tokens[0])
            except ValueError:
                print("Error: limit must be an integer.", file=sys.stderr)
                return

        state, pid = self._running_state()
        if pid is None:
            print("Warning: mock servers are not running (based on state). Attempting request anyway.")
        mgmt_url, _ = self._get_mgmt_url(state)

        try:
            response = requests.get(f"{mgmt_url}/endpoints?limit={limit}", timeout=10)
            response.raise_for_status()
            print(response.text)
        except requests.exceptions.RequestException as exc:
            print(f"Error fetching endpoints: {exc}", file=sys.stderr)

    def do_kill(self, arg):
        """Kill a specific server by its port.
        Usage: kill <port>"""
        tokens = self._split(arg)
        if len(tokens) != 1:
            print("Error: Please provide exactly one port number.", file=sys.stderr)
            return

        try:
            port = int(tokens[0])
        except ValueError:
            print("Error: Invalid port number.", file=sys.stderr)
            return

        try:
            response = requests.get(f"http://localhost:{port}/kill", timeout=10)
            response.raise_for_status()
            print(response.text)
        except requests.exceptions.RequestException as exc:
            print(f"Error killing server on port {port}: {exc}", file=sys.stderr)

    def do_revive(self, arg):
        """Revive a specific server by its port.
        Usage: revive <port>"""
        tokens = self._split(arg)
        if len(tokens) != 1:
            print("Error: Please provide exactly one port number.", file=sys.stderr)
            return

        try:
            port = int(tokens[0])
        except ValueError:
            print("Error: Invalid port number.", file=sys.stderr)
            return

        state, _ = self._running_state()
        mgmt_url, _ = self._get_mgmt_url(state)

        try:
            response = requests.get(f"{mgmt_url}/revive?port={port}", timeout=10)
            response.raise_for_status()
            print(response.json())
        except requests.exceptions.RequestException as exc:
            print(f"Error reviving server on port {port}: {exc}", file=sys.stderr)

    def do_scale(self, arg):
        """Scale up the number of servers.
        Usage: scale <num_servers>"""
        tokens = self._split(arg)
        if len(tokens) != 1:
            print("Error: Please provide the number of servers to add.", file=sys.stderr)
            return

        try:
            count = int(tokens[0])
        except ValueError:
            print("Error: Invalid number of servers.", file=sys.stderr)
            return

        state, _ = self._running_state()
        mgmt_url, _ = self._get_mgmt_url(state)

        try:
            response = requests.get(f"{mgmt_url}/scale?count={count}", timeout=10)
            response.raise_for_status()
            print(response.json())
        except requests.exceptions.RequestException as exc:
            print(f"Error scaling servers: {exc}", file=sys.stderr)

    def do_stats(self, arg):
        """Get statistics from the management API."""
        state, _ = self._running_state()
        mgmt_url, _ = self._get_mgmt_url(state)

        try:
            response = requests.get(f"{mgmt_url}/stats", timeout=10)
            response.raise_for_status()
            print(response.json())
        except requests.exceptions.RequestException as exc:
            print(f"Error fetching stats: {exc}", file=sys.stderr)

    def do_status(self, arg):
        """Check if the mock servers are running."""
        state, pid = self._running_state()

        if pid is not None:
            print(f"✓ Mock server process is running (PID {pid})")
            mgmt_url, mgmt_port = self._get_mgmt_url(state)
            try:
                response = requests.get(f"{mgmt_url}/stats", timeout=5)
                response.raise_for_status()
                stats = response.json()
                print(f"✓ Management API is responding on port {mgmt_port}")
                print(f"  - Current alive servers: {stats.get('current_alive', 0)}")
                print(f"  - Total started: {stats.get('total_started', 0)}")
                print(f"  - Total killed: {stats.get('total_killed', 0)}")
            except requests.exceptions.RequestException:
                print("✗ Management API is not responding")
        else:
            print("✗ Mock server process is not running")

    def do_logs(self, arg):
        """Show logs from the running process.
        Usage: logs [--tail <lines>]"""
        parser = argparse.ArgumentParser(prog='logs')
        parser.add_argument('--tail', type=int, default=50, help='Number of lines to show from the end (default: 50)')

        tokens = self._split(arg)
        try:
            args = parser.parse_args(tokens)
        except SystemExit:
            return

        state = self._load_state()
        if not state:
            print("No state information available. Start the servers first.", file=sys.stderr)
            return

        log_rel = state.get("log_file", "logs/mock-servers.log")
        log_path = self.base_dir / log_rel
        self._tail_file(log_path, args.tail)

    def do_exit(self, arg):
        """Exit the shell."""
        print("Thank you for using the mock server shell.")
        return True

    def do_quit(self, arg):
        """Exit the shell."""
        return self.do_exit(arg)


if __name__ == '__main__':
    MockServerShell().cmdloop()
