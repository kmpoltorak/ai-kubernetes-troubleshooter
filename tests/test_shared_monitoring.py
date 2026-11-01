"""Render the real Compose merge; no containers or credentials required."""
import json
import os
from pathlib import Path
import subprocess
import unittest

ROOT = Path(__file__).resolve().parent.parent


class SharedMonitoring(unittest.TestCase):
    def test_shared_mode_uses_only_central_monitoring_and_private_database(self):
        env = dict(os.environ, MONITORED_HTTP_PORT='18081', COMPOSE_PROFILES='')
        command = ['docker', 'compose', '--env-file', '/dev/null', '-f', 'docker-compose.yml',
                   '-f', 'compose.monitoring.yaml', 'config', '--format', 'json']
        result = subprocess.run(command, cwd=ROOT, env=env, text=True,
                                capture_output=True, check=True, timeout=30)
        model = json.loads(result.stdout)
        services = model['services']
        self.assertTrue({'prometheus', 'grafana', 'alertmanager'}.isdisjoint(services))
        app = services['app']
        self.assertEqual(set(app['networks']), {'default', 'monitoring'})
        self.assertIn('ai-kubernetes-troubleshooter', app['networks']['monitoring']['aliases'])
        self.assertEqual(app['ports'][0]['host_ip'], '127.0.0.1')
        self.assertEqual(app['ports'][0]['published'], '18081')
        self.assertTrue(model['networks']['monitoring']['external'])
        self.assertEqual(model['networks']['monitoring']['name'], 'kmpoltorak-monitoring-kubernetes-troubleshooter')
        for name, dependency in services.items():
            if name != 'app':
                self.assertFalse(dependency.get('ports'), name)
