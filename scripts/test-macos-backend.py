#!/usr/bin/env python3
"""Run native Swift session tests against disposable local Go/MySQL/Redis."""
import os
from pathlib import Path
import shutil
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


def run():
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
                test = '^TestMacOSNativeUIFixture$' if ui_fixture else '^TestMacOSClientSession$'
                timeout = '25m' if ui_fixture else '10m'
                subprocess.run(['go', 'test', '-tags=integration', '-count=1', f'-timeout={timeout}', '-v',
                                './integration', '-run', test],
                               cwd=ROOT / 'cloudnest_go', env=environment, check=True)
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
