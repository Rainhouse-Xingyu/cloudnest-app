#!/usr/bin/env python3
"""Run native Swift session tests against disposable local Go/MySQL/Redis."""
import os
import argparse
from pathlib import Path
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time

ROOT = Path(__file__).resolve().parents[1]


def available_port():
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        return listener.getsockname()[1]


def run_large_file_command(command, environment):
    # Own the Go/Swift process group so interruption or a Go watchdog cannot
    # leave an orphaned GiB writer after disposable services are stopped.
    process = subprocess.Popen(command, cwd=ROOT / 'cloudnest_go', env=environment, start_new_session=True)
    try:
        status = process.wait()
        if status:
            raise subprocess.CalledProcessError(status, command)
    finally:
        try:
            os.killpg(process.pid, signal.SIGTERM)
            deadline = time.monotonic() + 5
            while time.monotonic() < deadline:
                try:
                    os.killpg(process.pid, 0)
                except ProcessLookupError:
                    break
                time.sleep(0.05)
            else:
                os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            process.wait()


def run():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--filter', dest='test_filter')
    parser.add_argument('--go-tags', default='integration', help='Go build tags, including integration; optional real-model tests require aimodel')
    parser.add_argument('--mysql-time-zone', default='SYSTEM')
    parser.add_argument('--large-file', action='store_true', help='Run explicit 1 GiB + 173 byte, four-process native acceptance')
    parser.add_argument('--report', type=Path, help='Create an exclusive JSON report for --large-file')
    arguments = parser.parse_args()
    if 'integration' not in arguments.go_tags.split(','):
        parser.error('--go-tags must include integration')
    if arguments.large_file and (arguments.test_filter or os.environ.get('CLOUDNEST_NATIVE_UI_FIXTURE_DIR')):
        parser.error('--large-file cannot be combined with --filter or the manual UI fixture')
    if arguments.report and not arguments.large_file:
        parser.error('--report requires --large-file')
    if arguments.report and (arguments.report.exists() or not arguments.report.parent.is_dir()):
        parser.error('the report must name a new file in an existing directory')
    if arguments.large_file and shutil.disk_usage('/tmp').free < 8 * 1024**3:
        parser.error('--large-file requires at least 8 GiB of free temporary disk')
    if arguments.test_filter and os.environ.get('CLOUDNEST_NATIVE_UI_FIXTURE_DIR'):
        parser.error('--filter cannot be combined with the manual UI fixture')
    if sys.platform != 'darwin':
        raise RuntimeError('This integration test requires macOS and Swift 6.2 or newer')
    mysql_prefix = os.environ.get('CLOUDNEST_TEST_MYSQL_PREFIX') or subprocess.check_output(
        ['brew', '--prefix', 'mysql@8.4'], text=True).strip()
    mysqld = str(Path(mysql_prefix) / 'bin/mysqld')
    mysql = str(Path(mysql_prefix) / 'bin/mysql')
    redis = os.environ.get('CLOUDNEST_TEST_REDIS_BINARY') or shutil.which('redis-server')
    if redis is None:
        raise RuntimeError('Install redis with Homebrew before running this test')
    with tempfile.TemporaryDirectory(prefix='cloudnest-native-', dir='/tmp') as directory:
        directory = Path(directory)
        data = directory / 'mysql'
        mysql_socket = str(directory / 'mysql.sock')
        mysql_port, redis_port = available_port(), available_port()
        subprocess.run([mysqld, '--no-defaults', '--initialize-insecure', f'--basedir={mysql_prefix}', f'--datadir={data}'], check=True)
        processes = []
        with (directory / 'mysql.log').open('w+') as mysql_log, (directory / 'redis.log').open('w+') as redis_log:
            try:
                processes.append(subprocess.Popen([
                    mysqld, '--no-defaults', f'--basedir={mysql_prefix}', f'--datadir={data}', f'--socket={mysql_socket}',
                    f'--port={mysql_port}', '--bind-address=127.0.0.1', '--mysqlx=0',
                    f'--default-time-zone={arguments.mysql_time_zone}',
                    '--log-bin-trust-function-creators=1',
                    f'--pid-file={directory / "mysql.pid"}',
                ], stdout=mysql_log, stderr=subprocess.STDOUT))
                processes.append(subprocess.Popen([
                    redis, '--bind', '127.0.0.1', '--port', str(redis_port),
                    '--save', '', '--appendonly', 'no', '--dir', str(directory),
                ], stdout=redis_log, stderr=subprocess.STDOUT))
                mysql_command = [mysql, '--no-defaults', f'--socket={mysql_socket}', '-u', 'root']
                for _ in range(120):
                    if any(process.poll() is not None for process in processes):
                        raise RuntimeError('Disposable database process stopped during startup')
                    result = subprocess.run(mysql_command + ['-e', 'SELECT 1'], capture_output=True)
                    try:
                        with socket.create_connection(('127.0.0.1', redis_port), timeout=1):
                            redis_ready = True
                    except OSError:
                        redis_ready = False
                    if result.returncode == 0 and redis_ready:
                        break
                    time.sleep(1)
                else:
                    raise RuntimeError('Disposable services did not become ready')
                subprocess.run(mysql_command, input="""
CREATE DATABASE cloudnest_test;
CREATE USER 'cloudnest_test'@'127.0.0.1' IDENTIFIED BY 'cloudnest-disposable-test';
GRANT ALL PRIVILEGES ON cloudnest_test.* TO 'cloudnest_test'@'127.0.0.1';
""", text=True, check=True)
                environment = dict(os.environ,
                    CLOUDNEST_TEST_DISPOSABLE='1', CLOUDNEST_TEST_SWIFT='1',
                    CLOUDNEST_TEST_MYSQL_DSN=f'cloudnest_test:cloudnest-disposable-test@tcp(127.0.0.1:{mysql_port})/cloudnest_test?charset=utf8mb4&parseTime=True&loc=UTC',
                    CLOUDNEST_TEST_REDIS_ADDR=f'127.0.0.1:{redis_port}',
                )
                ui_fixture = os.environ.get('CLOUDNEST_NATIVE_UI_FIXTURE_DIR')
                if arguments.large_file:
                    environment['CLOUDNEST_NATIVE_LARGE_FILE'] = '1'
                    if arguments.report:
                        environment['CLOUDNEST_NATIVE_LARGE_FILE_REPORT'] = str(arguments.report.resolve())
                test = '^TestMacOSNativeLargeFile$' if arguments.large_file else ('^TestMacOSNativeUIFixture$' if ui_fixture else (arguments.test_filter or '^(TestMacOSClientSession|TestMacOSClientFRPRead|TestMacOSClientWorkspaceAccess|TestMacOSClientFileCollaboration|TestMacOSClientShares|TestMacOSClientSettings|TestMacOSPublicShareReceiver|TestMacOSNativeUIInterruptsActualHTTP)$'))
                timeout = '15m' if arguments.large_file else ('25m' if ui_fixture else '10m')
                command = ['go', 'test', f'-tags={arguments.go_tags}', '-count=1', f'-timeout={timeout}', '-v',
                           './integration', '-run', test]
                if arguments.large_file:
                    run_large_file_command(command, environment)
                else:
                    subprocess.run(command, cwd=ROOT / 'cloudnest_go', env=environment, check=True)
            except BaseException:
                for log in (mysql_log, redis_log):
                    log.flush()
                    log.seek(0)
                    print(log.read()[-12000:], file=sys.stderr)
                raise
            finally:
                for process in reversed(processes):
                    if process.poll() is None:
                        process.terminate()
                for process in reversed(processes):
                    try:
                        process.wait(timeout=30)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait()


if __name__ == '__main__':
    run()
