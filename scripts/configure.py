#!/usr/bin/env python3
"""Generate local Compose secrets without printing them or overwriting settings."""
import argparse
import os
from pathlib import Path
import secrets


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--import-hermes-key', action='store_true', help='Import only MDBLIST_API_KEY from the active Hermes profile .env')
    args = p.parse_args()
    root = Path(__file__).resolve().parents[1]
    target = root / '.env'
    if target.exists():
        raise SystemExit('.env already exists; preserved unchanged. Edit it or use web Settings.')
    key = ''
    if args.import_hermes_key:
        profile = Path(os.environ.get('HERMES_HOME', Path.home() / '.hermes'))
        path = profile / '.env'
        if path.is_file():
            for line in path.read_text().splitlines():
                if line.startswith('MDBLIST_API_KEY='):
                    key = line.split('=', 1)[1].strip().strip('\"\'')
                    break
        key = key or os.environ.get('MDBLIST_API_KEY', '')
        if not key:
            raise SystemExit('No MDBLIST_API_KEY found; no file was created.')
        if '\n' in key or '\r' in key:
            raise SystemExit('Invalid secret value; no file was created.')
    text = '\n'.join([
        'APP_PORT=8317', 'APP_BIND=127.0.0.1',
        'POSTGRES_DB=media_release_watcher', 'POSTGRES_USER=media_release_watcher',
        'POSTGRES_PASSWORD=' + secrets.token_hex(24),
        'APP_SECRET=' + secrets.token_hex(32),
        'MDBLIST_API_KEY=' + key, 'TZ=Europe/Amsterdam', 'PUBLIC_URL=', '',
    ])
    fd = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'w') as f:
        f.write(text)
    print('Created .env with new database/encryption secrets. MDBList key: ' + ('configured' if key else 'not yet configured'))
    print('Next: docker compose up -d. Create your login in the browser.')


if __name__ == '__main__':
    main()
