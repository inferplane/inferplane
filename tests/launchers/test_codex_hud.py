"""HUD selection is local-only and must not change unattended CLI behavior."""
import os
import json
import sys
import tempfile
from pathlib import Path
import runpy
import unittest
from unittest.mock import patch

LAUNCHER = Path(__file__).resolve().parents[2] / 'scripts/inferplane-codex'


class HUDTests(unittest.TestCase):
    def setUp(self):
        self.ns = runpy.run_path(str(LAUNCHER), run_name='launcher_test')

    def command(self, args, *, tty=True, env=None, available='/usr/bin/codex-hud'):
        with patch.dict(os.environ, env or {}, clear=True), \
                patch('sys.stdin.isatty', return_value=tty), \
                patch('sys.stdout.isatty', return_value=tty), \
                patch('shutil.which', return_value=available):
            return self.ns['codex_command'](args)

    def test_interactive_launch_resume_and_fork_use_hud(self):
        for args in [[], ['-m', 'model'], ['resume', '--last'], ['fork', 'id'],
                     ['-c', 'model="exec"', 'hello'], ['--', 'exec']]:
            with self.subTest(args=args):
                self.assertEqual(self.command(args),
                                 ['/usr/bin/codex-hud', 'start', '--language', 'ko', '--', *args])

    def test_noninteractive_and_management_commands_never_use_hud(self):
        for args in [['exec', '-'], ['-m', 'model', 'exec', '-'], ['debug', 'models'],
                     ['app-server'], ['review'], ['login'], ['--help'], ['--version'],
                     ['-c', 'x=y', 'doctor'], ['-C', 'exec', 'exec'], ['agents']]:
            with self.subTest(args=args):
                self.assertEqual(self.command(args), ['codex', *args])
        self.assertEqual(self.command([], tty=False), ['codex'])

    def test_missing_hud_and_explicit_disable_fall_back_without_changing_args(self):
        self.assertEqual(self.command(['resume'], available=None), ['codex', 'resume'])
        self.assertEqual(self.command([], env={'INFERPLANE_CODEX_HUD': 'off'}), ['codex'])

    def test_bad_modes_fail_without_echoing_untrusted_value(self):
        for env in [{'INFERPLANE_CODEX_HUD': 'private-value'},
                    {'INFERPLANE_CODEX_HUD_LANGUAGE': 'private-value'}]:
            with self.subTest(env=env):
                with self.assertRaises(self.ns['LauncherError']) as caught:
                    self.command([], env=env)
                self.assertNotIn('private-value', str(caught.exception))

    def test_run_codex_uses_selected_wrapper_preserving_environment_and_exit(self):
        ns = self.ns['run_codex'].__globals__
        args = ['resume', '--last', '-c', 'model_provider="inferplane"']
        child_env = {'INFERPLANE_API_KEY': 'offline-placeholder'}
        command = ['codex-hud', 'start', '--', *args]
        with patch.dict(ns, codex_command=lambda received: command if received == args else None,
                        codex_environment=lambda: child_env), \
                patch('subprocess.Popen') as popen:
            popen.return_value.wait.return_value = 7
            self.assertEqual(ns['run_codex'](args), 7)
            popen.assert_called_once_with(command, env=child_env)


    def test_real_wrapper_process_forwards_gateway_options_and_environment(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            record = root / 'record.json'
            hud = root / 'codex-hud'
            hud.write_text('#!' + sys.executable + '\n' +
                           'import os, sys\n' +
                           'assert sys.argv[1:5] == ["start", "--language", "ko", "--"]\n' +
                           'os.execvp("codex", ["codex", *sys.argv[5:]])\n')
            cli = root / 'codex'
            cli.write_text('#!' + sys.executable + '\n' +
                           'import os, sys, json, pathlib\n' +
                           'assert os.environ["INFERPLANE_API_KEY"] == "offline-placeholder"\n' +
                           'pathlib.Path(os.environ["RECORD"]).write_text(json.dumps(sys.argv[1:]))\n' +
                           'sys.exit(7)\n')
            hud.chmod(0o700)
            cli.chmod(0o700)
            args = ['resume', '--last', '-c', 'model_provider="inferplane"',
                    '-c', 'model_catalog_json="/temporary/catalog.json"',
                    '-c', 'approvals_reviewer="user"']
            env = {'PATH': directory, 'RECORD': str(record),
                   'INFERPLANE_API_KEY': 'offline-placeholder'}
            with patch.dict(os.environ, env, clear=True), \
                    patch('sys.stdin.isatty', return_value=True), \
                    patch('sys.stdout.isatty', return_value=True):
                self.assertEqual(self.ns['run_codex'](args), 7)
            self.assertEqual(json.loads(record.read_text()), args)

    def test_hud_language(self):
        self.assertEqual(self.command([], env={'INFERPLANE_CODEX_HUD_LANGUAGE': 'en'})[3], 'en')


if __name__ == '__main__':
    unittest.main()
