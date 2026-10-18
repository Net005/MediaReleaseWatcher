import test from 'node:test';
import assert from 'node:assert/strict';
import { escapeHTML, artworkURL, releaseWindow, parsePersonIdentity, acquisitionTarget, calendarCells, buildSettingsPayload, normalizeSearchRow, settingsMatch, bulkScope, summarizeBulkResult } from '../app.js';

test('artwork is always sent through allowlisted HTTPS proxy', () => {
  assert.equal(artworkURL('https://image.tmdb.org/t/p/w500/a.jpg'), '/api/artwork?url=https%3A%2F%2Fimage.tmdb.org%2Ft%2Fp%2Fw500%2Fa.jpg');
  for (const unsafe of ['javascript:alert(1)', 'http://image.tmdb.org/a', 'https://image.tmdb.org.evil/a', 'https://127.0.0.1/a', 'https://a:b@image.tmdb.org/a', 'https://image.tmdb.org:444/a', '', '/private']) assert.equal(artworkURL(unsafe), '');
});
test('release windows include today and all of the last thirty days', () => {
  const today = '2026-10-18';
  assert.equal(releaseWindow({release_date:'2026-10-18'}, today), 'current');
  assert.equal(releaseWindow({release_date:'2026-10-19'}, today), 'upcoming');
  assert.equal(releaseWindow({release_date:'2026-09-18'}, today), 'current');
  assert.equal(releaseWindow({release_date:'2026-09-17'}, today), 'past');
  assert.equal(releaseWindow({release_date:'unknown'}, today), 'unknown');
});
test('person identity accepts decimal tokens and explicit MDBList actor URLs without normalizing tokens', () => {
  assert.equal(parsePersonIdentity('001234'), '001234');
  assert.equal(parsePersonIdentity('https://mdblist.com/actor/1234'), '1234');
  for (const invalid of ['12x', '-4', '1e3', 'https://evil.test/actor/12', 'https://mdblist.com/actor/12?key=secret']) assert.throws(() => parsePersonIdentity(invalid));
});
test('MDBList filter URLs retain exactly one literal actor token', () => {
  assert.equal(parsePersonIdentity('https://mdblist.com/shows/?actor=235731&mediatype=show'), '235731');
  assert.equal(parsePersonIdentity('https://mdblist.com/movies/?actor=000235731'), '000235731');
  for (const value of ['https://mdblist.com/shows/?actor=12&actor=13','https://mdblist.com/shows/?actor=12&actor=12','https://mdblist.com/shows/?actor=%31%32','https://mdblist.com/shows/?actor=+12','https://mdblist.com/shows/?actor=12x','https://mdblist.com/shows/?actor=','https://mdblist.com/shows/?Actor=12','https://mdblist.com/shows/?actor=12&%61ctor=13','https://u:p@mdblist.com/shows/?actor=12','http://mdblist.com/shows/?actor=12','https://mdblist.com.evil/shows/?actor=12','https://mdblist.com/unrelated/?actor=12']) assert.throws(()=>parsePersonIdentity(value), value);
});

test('acquisition only routes compatible known media types', () => {
  assert.equal(acquisitionTarget({type:'movie'}), 'radarr');
  assert.equal(acquisitionTarget({type:'show'}), 'sonarr');
  assert.equal(acquisitionTarget({type:'unknown'}), null);
});
test('calendar is monday-aligned and has complete weeks', () => {
  const cells = calendarCells(2026, 1);
  assert.equal(cells.length % 7, 0);
  assert.equal(cells.filter(Boolean).length, 28);
  assert.equal(cells[6], '2026-02-01');
});
test('settings blank secrets are omitted; explicit toggles and numeric profiles retained', () => {
  const data = new Map([['mdblist.api_key',''], ['mdblist.list_ids','1,2'], ['mdblist.sync_interval_minutes','60'], ['sonarr.api_key','new-secret'],['sonarr.quality_profile_id','7'],['sonarr.root_folder','/shows'],['sonarr.url','https://arr.test']]);
  const result = buildSettingsPayload(data);
  assert.equal('api_key' in result.mdblist, false);
  assert.equal(result.sonarr.api_key, 'new-secret');
  assert.equal(result.sonarr.quality_profile_id, 7);
  assert.equal(result.sonarr.search, false);
  assert.equal(result.automation.enabled, false);
  assert.equal('configured' in result.sonarr, false);
});
test('initial baseline notifications remain disabled even with a stale form toggle', () => {
  const result=buildSettingsPayload(new Map([['automation.notify_first_sync','on']]));
  assert.equal(result.automation.notify_first_sync,false);
});

test('search rows map real provider IDs and never treat a local id as a TMDb id', () => {
  assert.deepEqual(normalizeSearchRow({title:'Film',mediatype:'movie',imdbid:'tt123',tmdbid:45}), {title:'Film', type:'movie', provider:'imdb', id:'tt123', year:null, poster:''});
  assert.equal(normalizeSearchRow({id:18,title:'Unknown',type:'movie'}).id, null);
});

test('settings readback compares explicit nonsecret values and tolerates credential redaction',()=>{
  assert.equal(settingsMatch({radarr:{enabled:true,root_folder:'/movies'}},{radarr:{enabled:true,root_folder:'/movies',api_key:'never-readable'}}),true);
  assert.equal(settingsMatch({radarr:{enabled:false}},{radarr:{enabled:true}}),false);
  assert.equal(settingsMatch({public_url:'http://one.test'},{public_url:'http://two.test'}),false);
});
test('bulk scopes exclude unknown dates except all and use supplied release dates only',()=>{
  const rows=[{id:1,release_date:'2026-10-18'},{id:2,release_date:'2026-10-19'},{id:3,digital_date:'2026-10-18'},{id:4,release_date:'2000-01-01'}];
  assert.deepEqual(bulkScope(rows,'all','2026-10-18').map(r=>r.id),[1,2,3,4]);
  assert.deepEqual(bulkScope(rows,'released','2026-10-18').map(r=>r.id),[1,4]);
  assert.deepEqual(bulkScope(rows,'upcoming','2026-10-18').map(r=>r.id),[2]);
  assert.throws(()=>bulkScope(rows,'invented'));
});
test('bulk outcomes preserve partial failure and reject inconsistent counts or duplicate identities',()=>{
  const data={results:[{release_id:1,ok:true,already_exists:false},{release_id:2,ok:true,already_exists:true},{release_id:3,ok:false,error:'Not configured'}],added:1,failed:1,skipped:3};
  const result=summarizeBulkResult(data);assert.equal(result.existing,1);assert.equal(result.excluded,2);assert.equal(result.total,5);assert.equal(result.results[2].error,'Not configured');
  assert.throws(()=>summarizeBulkResult({...data,added:2}));
  assert.throws(()=>summarizeBulkResult({...data,results:[...data.results,data.results[0]]}));
  assert.throws(()=>summarizeBulkResult({...data,skipped:0}));
});

test('escapes untrusted metadata in text and attributes', () => {
  assert.equal(escapeHTML(`<img src="x" onerror='bad'>&`), '&lt;img src=&quot;x&quot; onerror=&#39;bad&#39;&gt;&amp;');
  assert.equal(escapeHTML(null), '');
});
