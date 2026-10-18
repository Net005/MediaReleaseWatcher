#!/usr/bin/env python3
"""Exercise an isolated, NEW app/database. Never run against an existing install."""
import argparse
from datetime import date, timedelta
import http.cookiejar
import json
import secrets
import urllib.error
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--url', default='http://127.0.0.1:8317')
    args = parser.parse_args()
    base = args.url.rstrip('/')
    jar = http.cookiejar.CookieJar()
    client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))

    def call(method, path, body=None, expected=(200,)):
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(base + path, data=data, method=method,
                                     headers={'Content-Type': 'application/json', 'Origin': base})
        try:
            with client.open(req, timeout=30) as response:
                status, payload = response.status, response.read()
        except urllib.error.HTTPError as error:
            status, payload = error.code, error.read()
        if status not in expected:
            raise AssertionError(f'{method} {path}: expected {expected}, received HTTP {status}')
        return json.loads(payload) if payload else {}

    call('GET', '/healthz')
    session = call('GET', '/api/session')
    if not session.get('needs_setup'):
        raise SystemExit('Refusing to modify an existing installation; use a disposable empty database.')
    call('GET', '/api/settings', expected=(401,))
    password = secrets.token_urlsafe(30)
    call('POST', '/api/setup', {'password': password}, expected=(200, 201))
    assert call('GET', '/api/session')['authenticated']
    for path in ['/api/dashboard', '/api/releases', '/api/actresses', '/api/activity']:
        call('GET', path)
    settings = call('GET', '/api/settings')
    for section in ['mdblist', 'sonarr', 'radarr', 'silo', 'pushover']:
        for field in ['api_key', 'token', 'user']:
            assert not settings.get(section, {}).get(field), 'Secret leaked through settings API'
    assert not settings.get('automation', {}).get('enabled', False)
    call('GET', '/api/mdblist/lists')
    tasks = call('GET', '/api/tasks')['items']
    assert len(tasks) == 1 and tasks[0]['type'] == 'catalog_sync'
    task = call('POST', '/api/tasks', {
        'type': 'arr_sync', 'name': 'Synthetic paused inventory task', 'enabled': False,
        'frequency': 'once', 'date': (date.today() + timedelta(days=7)).isoformat(), 'time': '08:30',
        'timezone': 'Europe/Amsterdam', 'interval_minutes': 0, 'weekdays': [],
        'allow_download_search': False
    }, expected=(201,))
    tid = str(task['id'])
    assert task['enabled'] is False and task['timezone'] == 'Europe/Amsterdam'
    call('PATCH', '/api/tasks/' + tid, {'name': 'Edited paused inventory task'})
    task = next(item for item in call('GET', '/api/tasks')['items'] if str(item['id']) == tid)
    assert task['name'] == 'Edited paused inventory task' and task['enabled'] is False
    call('DELETE', '/api/tasks/' + tid, expected=(200, 204))
    assert all(str(item['id']) != tid for item in call('GET', '/api/tasks')['items'])
    source_entry = {'name': 'Synthetic smoke fixture', 'actor_id': '999999999999', 'group': 'Smoke test'}
    call('POST', '/api/actresses/import', {
        'group': 'Fallback smoke group', 'monitored': False, 'auto_add': True, 'notify': True,
        'entries': [source_entry, source_entry]
    }, expected=(200, 201, 202))
    actresses = call('GET', '/api/actresses')['items']
    actress = next(a for a in actresses if a['tmdb_id'] == '999999999999')
    assert sum(a['tmdb_id'] == '999999999999' for a in actresses) == 1
    assert actress['group'] == 'Smoke test' and actress['monitored'] is False
    assert actress['auto_add'] is True and actress['notify'] is True
    assert not call('GET', '/api/actresses?upcoming_only=true')['items']
    call('POST', '/api/actresses/import', {'entries': [source_entry], 'monitored': True,
         'auto_add': False, 'notify': False}, expected=(200, 201, 202))
    reread = next(a for a in call('GET', '/api/actresses')['items'] if a['tmdb_id'] == '999999999999')
    assert reread['monitored'] is False and reread['auto_add'] is True and reread['notify'] is True
    aid = str(actress['id'])
    detail = call('GET', '/api/actresses/' + aid)
    assert detail['actress']['name'] == 'Synthetic smoke fixture'
    call('PATCH', '/api/actresses/' + aid, {'monitored': False, 'group': 'Changed smoke group'})
    detail = call('GET', '/api/actresses/' + aid)
    assert detail['actress']['group'] == 'Changed smoke group'
    assert detail['actress']['monitored'] is False
    call('DELETE', '/api/actresses/' + aid, expected=(200, 204))
    assert all(a['tmdb_id'] != '999999999999' for a in call('GET', '/api/actresses')['items'])
    call('POST', '/api/logout', {})
    assert not call('GET', '/api/session')['authenticated']
    call('POST', '/api/login', {'password': password})
    assert call('GET', '/api/session')['authenticated']
    print('PASS: database readiness, setup/auth, catalogue APIs, redacted settings, IANA schedule CRUD, actress import defaults/deduplication/opt-out preservation/upcoming filter, actress CRUD and persistent login.')


if __name__ == '__main__':
    main()
