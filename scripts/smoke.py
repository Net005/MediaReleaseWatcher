#!/usr/bin/env python3
"""Exercise an isolated, NEW app/database. Never run against an existing install."""
import argparse
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
    for section in ['mdblist', 'sonarr', 'radarr', 'pushover']:
        for field in ['api_key', 'token', 'user']:
            assert not settings.get(section, {}).get(field), 'Secret leaked through settings API'
    assert not settings.get('automation', {}).get('enabled', False)
    call('POST', '/api/actresses/import', {'entries': [
        {'name': 'Synthetic smoke fixture', 'actor_id': '999999999999', 'group': 'Smoke test'}
    ]}, expected=(200, 201, 202))
    actresses = call('GET', '/api/actresses')['items']
    actress = next(a for a in actresses if a['tmdb_id'] == '999999999999')
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
    print('PASS: database readiness, setup/auth, catalogue APIs, safe settings, actress import/detail/edit/delete and persistent login.')


if __name__ == '__main__':
    main()
