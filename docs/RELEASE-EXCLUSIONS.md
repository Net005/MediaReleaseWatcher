# Verified release exclusions

Use only for an individually verified provider identity. Similar titles, genre,
country, or cast are not evidence. There is no automatic/fuzzy deduplication.

The durable `app_state` key is the literal concatenation
`release_exclusion:<media_type>:<tmdb_id>` (`show`, not `tv`). Key presence
excludes that identity, regardless of manual monitoring override. Removing the
key reverses the exclusion; it does not restore prior monitoring preferences.

Value contract (JSON object):

- `reason`: human-readable adjudication and limitations of the evidence.
- `canonical_tmdb_id`: string; returned in the excluded entry's HTTP 409 error.
- `season`, `episode`: integer canonical special reference, if applicable.
- `source_urls`: array of primary evidence URLs.
- Optional `verified_at`, `verified_by`, and other provenance fields are retained.

The key controls exclusion. Value fields document the decision; they do not
redirect lookups, change IDs, transfer cast, or authorize any merge. Keep the
original release row, source/list links, raw metadata and cast history.

After backup, an operator can seed **one** verified identity transactionally:

```sql
BEGIN;
-- Bind $1 = literal media_type, $2 = literal TMDb ID, $3 = JSON evidence object.
INSERT INTO app_state(key,value)
VALUES ('release_exclusion:' || $1 || ':' || $2, $3::jsonb);
UPDATE releases SET monitored=false, monitoring_override=false
WHERE media_type=$1 AND tmdb_id=$2;
COMMIT;
```

A conflict deliberately fails rather than silently overwriting an earlier
adjudication. Verify the exact key and archived row after the transaction.
Never populate secondary IDs from the canonical reference.

Public catalogue, actress counts/matches and cast-person filmographies omit the
identity. Direct detail/cast/monitoring/manual-acquire routes return HTTP 409 with
a canonical TMDb reference. Bulk acquisition uses the filtered matches; direct
acquisition also checks the ledger independently. Monitoring recomputation cannot
override exclusion. Automation omits new actions and retires queued actions;
scheduled missing-item searches recheck exclusion before external writes.

Metadata refresh can update the archived identity, but cannot make it visible or
monitored. TMDb credit processing resolves excluded identities without requesting
MDBList metadata or adding retry errors, including deferred missing titles. The
ledger survives ordinary application restarts; no migration seeds real IDs.
