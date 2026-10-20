export function escapeHTML(value) {
  return String(value ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
}
// Compatibility for stored metadata: retired diagnostics are historical, not
// current biography coverage. Explicit title source links remain untouched.
export function currentPersonMetadataStatus(status) {
  return String(status||'').split(' | ').filter(part=>!/tvmaze/i.test(part)).join(' | ') || 'Unknown / not enriched';
}
function personDiscoveryCoverage() {
  return '<div class="notice info"><strong>Discovery coverage: incomplete.</strong> Known source-linked releases only, not a complete filmography. 0 known matches does not mean no releases. MDBList biography availability does not establish discovery completeness. No matches are inferred. <span class="source-links"><a href="https://mdblist.com/" target="_blank" rel="noopener noreferrer">Metadata: MDBList</a></span></div>';
}
export function artworkURL(value) {
  try {
    const u = new URL(value);
    if (u.protocol !== 'https:' || !['image.tmdb.org','images.mdblist.com','mdblist.com'].includes(u.hostname) || u.username || u.password || u.port) return '';
    return '/api/artwork?url=' + encodeURIComponent(u.href);
  } catch { return ''; }
}
export function localDate(d = new Date()) {
  return `${d.getFullYear()}-${String(d.getMonth()+1).padStart(2,'0')}-${String(d.getDate()).padStart(2,'0')}`;
}
export function releaseDate(r) {
  const value = String(r.release_date || r.digital_date || '').slice(0,10);
  return /^\d{4}-\d{2}-\d{2}$/.test(value) && !Number.isNaN(Date.parse(value)) ? value : '';
}
export function releaseWindow(r, today = localDate()) {
  const date = releaseDate(r);
  if (!date) return 'unknown';
  const delta = (Date.parse(date) - Date.parse(today)) / 86400000;
  return delta > 0 ? 'upcoming' : delta >= -30 ? 'current' : 'past';
}
export function parsePersonIdentity(value) {
  if (/^\d+$/.test(value)) return value;
  const match = /^https:\/\/(?:www\.)?mdblist\.com\/(?:actor|person|people)\/(\d+)\/?$/.exec(value);
  if (match) return match[1];
  try {
    // Validate the literal spelling as well as parsed URL fields: URL parsing
    // must not repair whitespace, percent-encoded IDs, or duplicate parameters.
    const url=new URL(value);
    if(/^https:\/\/(?:www\.)?mdblist\.com\/(?:shows|movies)\/\?[^#]+$/.test(value) && url.protocol==='https:' && ['mdblist.com','www.mdblist.com'].includes(url.hostname) && !url.username && !url.password && !url.port && url.searchParams.getAll('actor').length===1) {
      const tokens=url.search.slice(1).split('&').filter(pair=>pair.startsWith('actor='));
      if(tokens.length===1&&/^actor=\d+$/.test(tokens[0]))return tokens[0].slice(6);
    }
  } catch {}
  throw new Error('Enter a decimal TMDb person ID, an MDBList actor URL, or an MDBList shows/movies filter URL with one literal actor ID. Source tokens are preserved exactly.');
}
export function acquisitionTarget(r) { return r.type === 'movie' ? 'radarr' : r.type === 'show' ? 'sonarr' : null; }
export function calendarCells(year, month) {
  const first = new Date(year, month, 1);
  const leading = (first.getDay() + 6) % 7;
  const days = new Date(year, month+1, 0).getDate();
  return Array.from({length:Math.ceil((leading+days)/7)*7}, (_,i) => i < leading || i >= leading+days ? null : `${year}-${String(month+1).padStart(2,'0')}-${String(i-leading+1).padStart(2,'0')}`);
}
export function buildSettingsPayload(data) {
  const get = name => data.get(name) ?? '';
  const bool = name => ['on','true',true].includes(get(name));
  const output = {
    mdblist:{list_ids:get('mdblist.list_ids'),sync_interval_minutes:Number(get('mdblist.sync_interval_minutes')) || 60},
    tmdb:{enabled:bool('tmdb.enabled')},
    automation:{enabled:bool('automation.enabled'),notify_first_sync:false},
    pushover:{enabled:bool('pushover.enabled'),device:get('pushover.device')},
    silo:{url:get('silo.url'),enabled:bool('silo.enabled')},
    public_url:get('public_url')
  };
  if(data.get('mdblist.list_count')!==null&&data.get('mdblist.list_count')!==undefined) {
    output.mdblist.lists=Array.from({length:Number(get('mdblist.list_count'))||0},(_,index)=>{
      const prefix=`mdblist.lists.${index}.`;
      return {id:String(get(prefix+'id')),name:String(get(prefix+'name')),enabled:bool(prefix+'enabled'),monitored:bool(prefix+'monitored'),auto_add:bool(prefix+'auto_add')};
    });
    delete output.mdblist.list_ids;
  }
  for (const kind of ['sonarr','radarr']) output[kind] = {url:get(`${kind}.url`),enabled:bool(`${kind}.enabled`),search:bool(`${kind}.search`),quality_profile_id:Number(get(`${kind}.quality_profile_id`)) || 0,root_folder:get(`${kind}.root_folder`)};
  for (const [section,field] of [['tmdb','api_key'],['silo','api_key'],['mdblist','api_key'],['sonarr','api_key'],['radarr','api_key'],['pushover','token'],['pushover','user']]) {
    const value = get(`${section}.${field}`);
    if (value) output[section][field] = value;
  }
  return output;
}
export function normalizeSearchRow(row) {
  const rawType = row.type || row.mediatype || row.media_type;
  const type = rawType === 'tv' || rawType === 'series' ? 'show' : rawType;
  const validID = (value, pattern) => (typeof value==='string'||(Number.isSafeInteger(value)&&value>0)) && pattern.test(String(value));
  const imdb = [row.imdb_id,row.imdbid,row.ids?.imdb].find(value=>validID(value,/^tt\d+$/));
  const tmdb = [row.tmdb_id,row.tmdbid,row.ids?.tmdb].find(value=>validID(value,/^\d+$/));
  return {title:row.title || row.name || 'Untitled',type,provider:imdb ? 'imdb' : 'tmdb',id:imdb ? String(imdb) : tmdb ? String(tmdb) : null,year:row.year || null,poster:row.poster || row.poster_url || ''};
}

const e = escapeHTML;
const $ = (s, root = document) => root.querySelector(s);
const $$ = (s, root = document) => [...root.querySelectorAll(s)];
const icon = (name, extra = '') => `<svg class="icon ${extra}" aria-hidden="true"><use href="#i-${name}"/></svg>`;
const names = {overview:'Overview',releases:'Releases',calendar:'Calendar',actresses:'Actresses',activity:'Activity',settings:'Settings'};
const state = {session:null,route:'overview',routeToken:0,releaseToken:0,actressToken:0,dashboard:null,settings:null,actresses:[],releases:new Map(),listPolicies:[],releaseFeed:null,filters:{q:'',window:'all',type:'all',group:'',list_id:'',sort:'release',page:1},actressQuery:'',actressGroup:'',actressUpcomingOnly:false,calendar:new Date(new Date().getFullYear(),new Date().getMonth(),1),calendarItems:[],polling:false,dirty:false};
const asArray = value => Array.isArray(value) ? value : [];
const taskTypes={catalog_sync:'Catalog discovery sync',silo_sync:'Silo availability sync',arr_sync:'Arr inventory sync',search_missing:'Search app-monitored missing releases'};
const weekdays=['Sunday','Monday','Tuesday','Wednesday','Thursday','Friday','Saturday'];
let taskItems=new Map();
const count = value => Number.isFinite(Number(value)) ? Number(value).toLocaleString() : '—';
const mediaName = type => type === 'show' ? 'Series' : type === 'movie' ? 'Movie' : 'Unknown type';
const targetName = target => target === 'radarr' ? 'Radarr' : 'Sonarr';
function formatDate(value, options = {day:'numeric',month:'short',year:'numeric'}) {
  if (!value) return 'Date not known';
  const d = /^\d{4}-\d{2}-\d{2}$/.test(value) ? new Date(value+'T12:00:00') : new Date(value);
  return Number.isNaN(d.getTime()) ? 'Date not known' : options.timeStyle?d.toLocaleString(undefined,options):d.toLocaleDateString(undefined, options);
}
function remember(releases) { asArray(releases).forEach(r => state.releases.set(String(r.id),r)); }
function debounce(fn, delay = 280) { let timer; return (...args) => { clearTimeout(timer);timer=setTimeout(() => fn(...args),delay); }; }
function safeExternal(value) { try {const u=new URL(value);return ['https:','http:'].includes(u.protocol) && !u.username && !u.password ? u.href : '';} catch{return '';} }
function art(url, title = '', kind = 'film', extra = '') {
  const src = artworkURL(url);
  return `<div class="art ${src?'':'missing'} ${extra}">${icon(kind)}<span class="art-label">${kind==='user'?'Portrait unavailable':'Artwork unavailable'}</span>${src?`<img src="${e(src)}" alt="${e(title)}" loading="lazy" decoding="async">`:''}</div>`;
}
function empty(title, message, {name='film',actions='',error=false} = {}) {
  return `<div class="empty-state ${error?'error-state':''}">${icon(name)}<h3>${e(title)}</h3><p>${e(message)}</p>${actions?`<div class="actions">${actions}</div>`:''}</div>`;
}
function loading(message = 'Loading your collection…') { return `<div class="inline-loading" role="status"><span class="spinner"></span>${e(message)}</div>`; }
function errorView(error, action='retry') { return empty('Couldn’t load this view',error.message || 'The server could not be reached.',{name:'info',error:true,actions:`<button class="btn" data-action="${action}">${icon('refresh')}Try again</button>`}); }
function heading(title, subtitle, actions='') { return `<div class="page-heading"><div><h1>${e(title)}</h1><p>${e(subtitle)}</p></div>${actions || `<div class="heading-date"><b>${e(new Date().toLocaleDateString(undefined,{weekday:'long'}))}</b><span>${e(formatDate(localDate()))}</span></div>`}</div>`; }
function toast(message, error=false) {
  const node=document.createElement('div');node.className='toast'+(error?' error':'');
  node.innerHTML=`${icon(error?'info':'check')}<span>${e(message)}</span><button aria-label="Dismiss notification">${icon('close')}</button>`;
  $('button',node).addEventListener('click',()=>node.remove());$('#toasts').append(node);setTimeout(()=>node.remove(),error?12000:6500);
}
async function api(path, {method='GET',body,timeout=45000,signal}={}) {
  const controller=new AbortController();const timer=setTimeout(()=>controller.abort(),timeout);const abort=()=>controller.abort();signal?.addEventListener('abort',abort,{once:true});
  try {
    const response=await fetch(path,{method,credentials:'same-origin',headers:{Accept:'application/json',...(body!==undefined?{'Content-Type':'application/json'}:{})},body:body!==undefined?JSON.stringify(body):undefined,signal:controller.signal});
    let data;try {data=await response.json();} catch {throw new Error(response.ok?'The server returned an unreadable response.':`Request failed (${response.status}).`);}
    if(!response.ok){if(response.status===401 && !['/api/login','/api/setup','/api/session'].includes(path))showAuth(false);throw new Error(data.error || `Request failed (${response.status}).`);}return data;
  } catch(error){if(error.name==='AbortError')throw new Error('This request timed out. Its server-side operation may still finish; check activity before retrying.');throw error;}
  finally {clearTimeout(timer);signal?.removeEventListener('abort',abort);}
}
async function busy(button, task, text='Working…') {
  const old=button?.innerHTML;if(button){button.disabled=true;button.innerHTML=`<span class="spinner"></span>${e(text)}`;}
  try{return await task();}finally{if(button?.isConnected){button.disabled=false;button.innerHTML=old;}}
}
function makeDialog(title, html, {wide=false,narrow=false}={}) {
  const previous=document.activeElement;const dialog=document.createElement('dialog');dialog.className=`dialog ${wide?'wide':''} ${narrow?'narrow':''}`;
  const titleId=`dialog-title-${crypto.randomUUID()}`;dialog.setAttribute('aria-labelledby',titleId);
  dialog.innerHTML=`<header class="dialog-header"><h2 id="${titleId}">${e(title)}</h2><button class="icon-button close-dialog" aria-label="Close dialog">${icon('close')}</button></header><div class="dialog-body">${html}</div>`;
  $('.close-dialog',dialog).addEventListener('click',()=>{if(dialog.dataset.pending!=='true')dialog.close();});
  dialog.addEventListener('cancel',event=>{if(dialog.dataset.pending==='true')event.preventDefault();});
  dialog.addEventListener('close',()=>{dialog.remove();if(previous?.isConnected)previous.focus({preventScroll:true});},{once:true});
  dialog.addEventListener('click',event=>{if(event.target===dialog&&dialog.dataset.pending!=='true'){const r=dialog.getBoundingClientRect();if(event.clientX<r.left||event.clientX>r.right||event.clientY<r.top||event.clientY>r.bottom)dialog.close();}});
  $('#dialogs').append(dialog);dialog.showModal();const autofocus=$('[autofocus]',dialog);if(autofocus)autofocus.focus();return dialog;
}
function dialogError(dialog,error) {let node=$('.form-error',dialog);if(!node){node=document.createElement('p');node.className='form-error';node.setAttribute('role','alert');$('.dialog-body',dialog).append(node);}node.hidden=false;node.textContent=error.message;}
function closeDialogs() { $$('dialog').forEach(d=>d.close()); }
function showAuth(needsSetup) {
  state.session={authenticated:false,needs_setup:needsSetup};state.routeToken++;closeDialogs();$('#boot').hidden=true;$('#app').hidden=true;$('#auth').hidden=false;
  $('#auth-title').textContent=needsSetup?'Make yourself at home.':'Welcome back.';$('#auth-subtitle').textContent=needsSetup?'Create a password to secure your private release radar.':'Sign in to your release radar.';
  $('#auth-submit').innerHTML=`${needsSetup?'Create your account':'Sign in'} ${icon('arrow')}`;
  $('#auth-password').autocomplete=needsSetup?'new-password':'current-password';$('#auth-password').minLength=needsSetup?8:1;$('#auth-confirm-label').hidden=!needsSetup;$('#auth-confirm').required=needsSetup;
  $('#auth-password').value='';$('#auth-confirm').value='';$('#auth-error').hidden=true;$('#auth-password').focus();
}
async function authenticate(event) {
  event.preventDefault();const password=$('#auth-password').value;const setup=state.session?.needs_setup;const error=$('#auth-error');error.hidden=true;
  if(setup && password!==$('#auth-confirm').value){error.textContent='The passwords don’t match.';error.hidden=false;return;}
  await busy($('#auth-submit'),async()=>{try{await api(setup?'/api/setup':'/api/login',{method:'POST',body:{password}});let session=await api('/api/session');if(setup&&!session.authenticated){await api('/api/login',{method:'POST',body:{password}});session=await api('/api/session');}if(!session.authenticated)throw new Error('Sign-in did not create a session. Check that cookies are enabled.');state.session=session;$('#auth-password').value='';$('#auth-confirm').value='';await startApp();}catch(err){error.textContent=err.message;error.hidden=false;}},setup?'Creating account…':'Signing in…');
}
async function startApp() {$('#boot').hidden=true;$('#auth').hidden=true;$('#app').hidden=false;await Promise.all([refreshActressCache().catch(error=>toast(error.message,true)),api('/api/mdblist/lists').then(data=>{state.listPolicies=asArray(data.items);}).catch(error=>toast(error.message,true))]);await navigate();}
function updateSync(sync={}) {const status=$('#sync-status');status.className='sync-chip '+(sync.running?'running':sync.last_error?'error':sync.last_run?'ready':'');status.title=sync.last_error || (sync.last_run?`Last synced ${formatDate(sync.last_run)}`:'No completed sync yet');status.innerHTML=`<i></i><span>${e(sync.running?'Syncing':sync.last_error?'Sync issue':sync.last_run?'Up to date':'Not synced')}</span>`;}
async function getDashboard() {const data=await api('/api/dashboard');state.dashboard=data;remember([...(data.featured||[]),...(data.recent||[]),...(data.upcoming||[])]);updateSync(data.sync);$('#nav-release-count').textContent=data.stats?.releases!==undefined?count(data.stats.releases):'';$('#nav-actress-count').textContent=data.stats?.actresses!==undefined?count(data.stats.actresses):'';return data;}
async function navigate() {
  if(!state.session?.authenticated)return;const path=location.hash.replace(/^#\/?/,'').split('?')[0];const deepRelease=/^release\/(\d+)$/.exec(path);state.route=deepRelease?'releases':names[path]?path:'overview';const token=++state.routeToken;state.releaseToken++;state.actressToken++;
  $$('[data-nav]').forEach(a=>{if(a.dataset.nav===state.route)a.setAttribute('aria-current','page');else a.removeAttribute('aria-current');});$('#breadcrumb').textContent=names[state.route];document.title=`${names[state.route]} · Media Release Watcher`;
  $('#main').innerHTML=`<div class="page-loading" role="status"><span class="spinner"></span><p>Loading ${e(names[state.route].toLowerCase())}…</p></div>`;
  try {
    if(state.route==='overview'){const data=await getDashboard();if(token!==state.routeToken)return;renderOverview(data);await loadReleases();}
    else if(state.route==='releases'){renderReleasesPage();await loadReleases();}
    else if(state.route==='actresses'){renderActressesPage();await loadActresses();}
    else if(state.route==='calendar'){renderCalendarPage();await loadCalendar();}
    else if(state.route==='activity'){const [data,dashboard]=await Promise.all([api('/api/activity'),getDashboard()]);if(token!==state.routeToken)return;renderActivity(data,dashboard);}
    else if(state.route==='settings'){const settings=await api('/api/settings');if(token!==state.routeToken)return;state.settings=settings;state.dirty=false;renderSettings(settings);}
    if(deepRelease&&token===state.routeToken)await openRelease(deepRelease[1]);
  }catch(error){if(token===state.routeToken&&state.session?.authenticated)$('#main').innerHTML=errorView(error);}
}
function statsBar(stats={}) {return `<div class="stats-bar">${[['releases','film','Releases in your radar',''],['upcoming','calendar','Upcoming releases','gold'],['actresses','users','Actresses followed',''],['monitored','monitor','Active actress watches','teal']].map(([key,name,label,color])=>`<div class="stat ${color}">${icon(name)}<div><b>${stats[key]===undefined?'—':count(stats[key])}</b><small>${label}</small></div></div>`).join('')}</div>`;}
function hero(releases) {
  const featured=asArray(releases);const r=featured.find(r=>artworkURL(r.backdrop))||featured[0];
  if(!r)return `<section class="hero empty-hero"><span class="hero-corner">YOUR RELEASE RADAR</span><div class="hero-content"><p class="hero-kicker">A collection worth following</p><h2>Good things<br>are on the horizon.</h2><p class="hero-description">Start with a title, a list, or an actress you follow. Real releases and artwork will find their home here. Nothing added. Nothing invented.</p><div class="actions"><button class="btn primary" data-action="search">${icon('plus')}Find your first title</button><a class="btn ghost" href="#/settings">Connect MDBList ${icon('arrow')}</a></div></div></section>`;
  const date=releaseDate(r);const backdrop=artworkURL(r.backdrop);const window=releaseWindow(r);
  return `<section class="hero">${backdrop?`<img class="hero-art" src="${e(backdrop)}" alt="" fetchpriority="high">`:''}<span class="hero-corner">IN YOUR RADAR</span><div class="hero-content"><p class="hero-kicker">${window==='upcoming'?'Coming into view':'From your collection'}</p><div class="hero-meta"><span class="badge ${window==='upcoming'?'gold':''}">${e(mediaName(r.type))}</span><span>${e(r.year||'Year unknown')}</span>${r.certification?`<span class="badge">${e(r.certification)}</span>`:''}${r.score!==null&&r.score!==undefined?`<span>${icon('star')} ${e(r.score)} MDBList</span>`:''}</div><h2>${e(r.title||'Untitled release')}</h2><p class="hero-description">${e(r.description||'A release in your personal collection. Open the details to see available metadata and matched actresses.')}</p><div class="actions"><button class="btn primary" data-action="release" data-id="${e(r.id)}">Explore release ${icon('arrow')}</button>${acquisitionTarget(r)?`<button class="btn ghost" data-action="acquire" data-id="${e(r.id)}">${icon('plus')}${targetName(acquisitionTarget(r))}</button>`:''}</div>${date?`<p class="field-help">${window==='upcoming'?'Expected':'Release date'} ${e(formatDate(date))}</p>`:''}</div></section>`;
}
function horizon(items) {return `<aside class="horizon" aria-label="Upcoming highlights"><div class="horizon-header"><h3>On the horizon</h3>${icon('calendar')}</div>${items.length?items.slice(0,3).map(r=>`<button class="horizon-row" data-action="release" data-id="${e(r.id)}">${art(r.poster,'')}<span><b>${e(r.title)}</b><small>${e(formatDate(releaseDate(r),{month:'short',day:'numeric'}))}</small></span></button>`).join(''):'<p>No upcoming releases yet.<br>Connect your lists and watches to see what’s next.</p>'}<a href="#/calendar" class="text-button">Open release calendar ${icon('arrow')}</a></aside>`;}
function renderOverview(data) {$('#main').innerHTML=heading('Your release radar.','The titles and people you follow. All in one place.')+`<div class="feature-layout">${hero(data.featured)}${horizon(asArray(data.upcoming))}</div>${statsBar(data.stats)}${releaseSection('In the frame','Browse every release in your collection.')}<div class="overview-bottom"><section class="panel"><div class="section-heading"><h2>Latest activity</h2><a href="#/activity" class="text-button">View all ${icon('arrow')}</a></div><div id="overview-activity">${activityList(asArray(data.activity).slice(0,4))}</div></section><section class="panel sync-panel" id="overview-sync">${syncPanel(data.sync)}</section></div>`;bindReleaseControls();}
function releaseSection(title='Your collection',subtitle='') {
  const stats=state.dashboard?.stats||{};return `<section aria-labelledby="release-section-title"><div class="section-heading"><div><h2 id="release-section-title">${e(title)}</h2>${subtitle?`<p>${e(subtitle)}</p>`:''}</div><span id="release-total"></span></div><div class="release-tabs" aria-label="Release window">${[['all','All releases'],['current','Current'],['upcoming','Upcoming'],['past','Past']].map(([key,label])=>`<button class="${key==='upcoming'?'gold':''}" data-window="${key}" aria-pressed="${state.filters.window===key}">${label}${stats[key==='all'?'releases':key]!==undefined?`<small>${count(stats[key==='all'?'releases':key])}</small>`:''}</button>`).join('')}</div><div class="toolbar"><label class="search-field">${icon('search')}<span class="visually-hidden">Search your releases</span><input type="search" id="release-query" placeholder="Search your collection…" value="${e(state.filters.q)}"></label><select id="release-type" aria-label="Media type"><option value="all">All types</option><option value="movie" ${state.filters.type==='movie'?'selected':''}>Movies</option><option value="show" ${state.filters.type==='show'?'selected':''}>Series</option></select>${groupSelect('release-group',state.filters.group)}<select id="release-list" aria-label="Filter by personal list"><option value="">All personal lists</option>${state.listPolicies.map(row=>`<option value="${e(row.id)}" ${row.id===state.filters.list_id?'selected':''}>${e(row.name||row.id)}</option>`).join('')}</select><select id="release-sort" aria-label="Sort releases">${[['release','Release date'],['added','Recently added'],['score','MDBList score'],['title','Title A–Z']].map(([key,label])=>`<option value="${key}" ${key===state.filters.sort?'selected':''}>${label}</option>`).join('')}</select><label class="size-control" title="Cover size">${icon('grid')}<span class="visually-hidden">Cover size</span><input id="cover-size" type="range" min="140" max="280" value="174" step="10"></label></div><div id="release-summary" class="result-summary"></div><div id="release-grid" class="cover-grid" aria-live="polite">${loading()}</div><div id="release-pagination"></div></section>`;
}
function groupSelect(id,selected='') {const groups=[...new Set(state.actresses.map(a=>a.group).filter(Boolean))].sort();if(selected&&!groups.includes(selected))groups.push(selected);return `<select id="${id}" aria-label="Filter by group"><option value="">All groups</option>${groups.map(g=>`<option value="${e(g)}" ${g===selected?'selected':''}>${e(g)}</option>`).join('')}</select>`;}
function renderReleasesPage() {$('#main').innerHTML=heading('The collection.','Movies and series, with the details that matter.',`<div class="actions"><button class="btn primary" data-action="search">${icon('plus')}Add a title</button></div>`)+releaseSection('Your releases');bindReleaseControls();}
function bindReleaseControls() {
  const search=debounce(input=>{if(input.isConnected)loadReleases();});
  $('#release-query')?.addEventListener('input',event=>{state.filters.q=event.target.value;cancelReleaseLoad();search(event.target);});
  [['#release-type','type'],['#release-group','group'],['#release-list','list_id'],['#release-sort','sort']].forEach(([selector,key])=>$(selector)?.addEventListener('change',event=>{state.filters[key]=event.target.value;loadReleases();}));
  $$('[data-window]').forEach(button=>button.addEventListener('click',()=>{state.filters.window=button.dataset.window;$$('[data-window]').forEach(b=>b.setAttribute('aria-pressed',String(b===button)));loadReleases();}));
  $('#cover-size')?.addEventListener('input',event=>$('#release-grid')?.style.setProperty('--cover-min',`${event.target.value}px`));
}
function cancelReleaseLoad() {
  state.releaseToken++;state.releaseFeed?.controller?.abort();state.releaseFeed?.observer?.disconnect();state.releaseFeed=null;
}
function releaseMoreControls(feed,error=null) {
  const node=$('#release-pagination');if(!node)return;feed.observer?.disconnect();
  node.innerHTML=error?`<p class="form-error" role="alert">${e(error.message)}</p><button class="btn" data-action="more-releases">Try loading more</button>`:feed.loading?loading('Loading more releases…'):feed.done?'<p class="field-help" role="status">All matching releases loaded.</p>':'<button class="btn" data-action="more-releases">Load more releases</button><div id="release-sentinel" aria-hidden="true"></div>';
  if(!error&&!feed.loading&&!feed.done&&typeof IntersectionObserver!=='undefined'){
    feed.observer=new IntersectionObserver(entries=>{if(entries.some(entry=>entry.isIntersecting)&&state.releaseFeed===feed&&!feed.loading)loadReleases(true);},{rootMargin:'250px'});feed.observer.observe($('#release-sentinel'));
  }
}
async function loadReleases(append=false) {
  const grid=$('#release-grid');if(!grid)return;
  if(!append){cancelReleaseLoad();state.filters.page=1;state.releaseFeed={items:new Map(),page:0,done:false,loading:false,controller:new AbortController()};grid.innerHTML=loading('Loading releases…');}
  const feed=state.releaseFeed;if(!feed||feed.loading||feed.done)return;
  const token=state.releaseToken,routeToken=state.routeToken,page=feed.page+1;feed.loading=true;grid.setAttribute('aria-busy','true');releaseMoreControls(feed);
  const query=new URLSearchParams({...state.filters,page,limit:state.route==='overview'?12:36});
  try{
    const data=await api('/api/releases?'+query,{signal:feed.controller.signal});if(token!==state.releaseToken||routeToken!==state.routeToken)return;
    const items=asArray(data.items),total=Number(data.total),limit=Number(data.limit);
    if(!Number.isSafeInteger(total)||total<0||!Number.isSafeInteger(limit)||limit<1||Number(data.page)!==page)throw new Error('The server returned invalid release pagination.');
    remember(items);const added=items.filter(r=>!feed.items.has(String(r.id))&&(feed.items.set(String(r.id),r),true));
    if(!feed.page)grid.innerHTML='';grid.insertAdjacentHTML('beforeend',added.map(releaseCard).join(''));feed.page=page;
    feed.done=items.length===0||page*limit>=total;feed.loading=false;
    $('#release-total').textContent=`${count(total)} releases`;$('#release-summary').textContent=`${feed.items.size} shown · ${count(total)} matching releases · MDBList metadata`;
    if(!feed.items.size)grid.innerHTML=empty('A little quiet, for now.','No releases match these filters, or no sources have been synced yet.',{actions:'<button class="btn" data-action="clear-filters">Clear filters</button><button class="btn primary" data-action="search">Add title</button>'});
    releaseMoreControls(feed);
  }catch(error){if(token===state.releaseToken&&routeToken===state.routeToken){feed.loading=false;if(!feed.page){grid.innerHTML=errorView(error,'retry-releases');$('#release-pagination').innerHTML='';}else releaseMoreControls(feed,error);}}
  finally{if(grid.isConnected&&token===state.releaseToken)grid.removeAttribute('aria-busy');}
}
function hasAcquisition(r,target) { return ['added','already_exists'].includes(r.acquisition?.[target]); }
function monitoringReason(r) {
  if(r.monitoring_origin==='manual')return r.monitored?'Manual override: monitored':'Manual override: not monitored';
  if(r.monitoring_origin==='actress')return 'Actress watch rule (Auto-add / Notify)';
  if(r.monitoring_origin==='list')return 'Personal list monitoring rule';
  return r.monitored?'Monitored (reason not supplied)':'Not monitored (discovery only)';
}
function releaseSourceMarkup(r) {
  const lists=asArray(r.lists);
  return `<div class="release-provenance"><span>${e(monitoringReason(r))}</span><small>Sources: ${lists.length?lists.map(row=>`${e(row.name||row.id)} · Monitor ${row.monitored?'on':'off'} / Auto-add ${row.auto_add?'on':'off'}`).join('; '):e(r.source||'Manual / source not supplied')}</small></div>`;
}
function arrPresenceMarkup(r) {
  const target=acquisitionTarget(r);if(!target)return '';const value=r.acquisition||{},name=targetName(target),error=value[target+'_error'];
  const status=error?'unknown':value[target+'_status']==='present'?'present':value[target+'_status']==='absent'?'absent':'unknown';
  const ready=status==='present'&&value[target+'_ready']==='true',files=Number(value[target+'_file_count']);
  const href=safeExternal(value[target+'_url']);
  return `<div class="arr-presence"><span class="badge ${status==='present'?'teal':''}">${name} inventory: ${status}</span>${status==='present'?`<span class="badge ${ready?'teal':''}">${name} readiness: ${ready?'ready'+(Number.isSafeInteger(files)&&files>=0?` · ${files} files`:''):'not ready'}</span>`:''}${error?`<small class="form-error">${e(error)}</small>`:''}${href?`<a href="${e(href)}" target="_blank" rel="noopener noreferrer">Open ${name}</a>`:''}</div>`;
}
function availabilityMarkup(r) {
  const info=r.local_availability||{},available=info.status==='available';
  const label=available?'In Silo':info.status==='absent'?'Not in Silo':info.status==='error'?'Silo lookup failed':'Silo presence unknown';
  const href=safeExternal(info.url);
  return `<div class="local-availability"><span class="badge ${available?'teal':''}" title="${e(info.error||('Silo check: '+(info.checked_at?formatDate(info.checked_at):'not yet checked')))}">${e(label)}</span>${href?`<a href="${e(href)}" target="_blank" rel="noopener noreferrer">Open in Silo</a>`:''}</div>`;
}
async function loadReleaseCast(dialog,id) {
  const node=$('[data-release-cast]',dialog);if(!node)return;node.innerHTML=loading('Loading supplied cast…');
  try {
    const data=await api(`/api/releases/${encodeURIComponent(id)}/cast`);if(!node.isConnected||!dialog.open)return;
    const rows=asArray(data.items);node.innerHTML=`${rows.length?`<div class="cast-grid">${rows.map(person=>{const valid=typeof person.tmdb_id==='string'&&/^\d+$/.test(person.tmdb_id);return `<button class="cast-card" data-action="person" data-id="${e(person.tmdb_id)}" aria-label="View person ${e(person.name)}" ${valid?'':'disabled'}>${art(person.profile,'','user')}<b>${e(person.name||'Name not supplied')}</b><small>${e(person.character||'Role not supplied')}</small></button>`;}).join('')}</div>`:'<p class="field-help">Cast not supplied. Unknown coverage is not an empty cast.</p>'}<p class="field-help">${e(data.coverage||'Coverage unknown / may be incomplete.')}</p>`;
  }catch(error){if(node.isConnected&&dialog.open)node.innerHTML=`<p class="form-error" role="alert">${e(error.message)}</p><button class="btn small" data-action="cast-retry" data-release-id="${e(id)}">Retry cast</button>`;}
}
async function openPerson(id) {
  if(!/^\d+$/.test(id))throw new Error('A literal TMDb person ID is required.');
  const dialog=makeDialog('Person details',loading('Loading source person details…'),{wide:true});
  try{const data=await api(`/api/people/${encodeURIComponent(id)}`);if(!dialog.open)return;const p=data.person||{},items=asArray(data.releases);remember(items);$('.dialog-body',dialog).innerHTML=`<div class="actress-detail-heading">${art(p.profile,p.name,'user')}<div><h2>${e(p.name||'Name not supplied')}</h2><dl class="detail-facts">${detailFact('TMDb person ID',p.tmdb_id)}${birthdayFact(p)}${detailFact('Place of birth',p.place_of_birth)}${detailFact('MDBList biography status',currentPersonMetadataStatus(p.metadata_status))}</dl></div></div><p class="biography">${e(p.biography||'Biography not supplied.')}</p>${personDiscoveryCoverage()}<h3>Known releases</h3><p class="field-help">Opening a person does not create a watch.</p><div class="cover-grid">${items.map(releaseCard).join('')}</div>`;}
  catch(error){if(dialog.open)$('.dialog-body',dialog).innerHTML=empty('Person details unavailable',error.message,{error:true,actions:`<button class="btn" data-person-retry>Try again</button>`});$('[data-person-retry]',dialog)?.addEventListener('click',()=>{dialog.close();openPerson(id);},{once:true});}
  return dialog;
}
function releaseCard(r) {
  const window=releaseWindow(r);const target=acquisitionTarget(r);const acquired=target&&hasAcquisition(r,target);const date=releaseDate(r);const age=r.age_rating!==null&&r.age_rating!==undefined?Number(r.age_rating):null;
  return `<article class="release-card"><button class="cover-link" data-action="release" data-id="${e(r.id)}" aria-label="View ${e(r.title||'release')}">${art(r.poster,'')}<span class="cover-overlay"><span class="badge ${window==='upcoming'?'gold':window==='current'?'teal':''}">${window==='upcoming'?'Upcoming':window==='current'?'Released':window==='past'?'Past release':'Date unknown'}</span>${r.certification?`<span class="badge ${age>=18?'red':''}">${e(r.certification)}</span>`:age!==null?`<span class="badge ${age>=18?'red':''}">Age ${e(age)}+</span>`:''}</span><span class="cover-bottom">${r.monitored?`<span class="badge teal">${icon('monitor')}Monitored</span>`:'<span></span>'}<span class="badge">${e(r.year||'—')}</span></span></button><div class="card-body"><button class="card-title-button" data-action="release" data-id="${e(r.id)}"><h3>${e(r.title||'Untitled release')}</h3></button><div class="card-meta"><span>${e(mediaName(r.type))}</span><span>·</span><span>${date?e(formatDate(date,{month:'short',day:'numeric'})):'Date unknown'}</span>${r.score!==null&&r.score!==undefined?`<span class="score">${e(r.score)} score</span>`:''}</div><div class="card-genres">${e(asArray(r.genres).slice(0,3).join(' / ')||'Genres not supplied')}</div>${releaseSourceMarkup(r)}${arrPresenceMarkup(r)}${availabilityMarkup(r)}<div class="card-actions">${target?`<button class="btn ${acquired?'acquired':''}" data-action="acquire" data-id="${e(r.id)}">${icon(acquired?'check':'plus')}${acquired?'Monitor & search in '+targetName(target):'Add to '+targetName(target)}</button>`:`<button class="btn" disabled>Type unavailable</button>`}<button class="btn watch-toggle ${r.monitored?'on':''}" data-action="monitor" data-id="${e(r.id)}" aria-label="${r.monitored?'Stop monitoring':'Monitor'} ${e(r.title)}" aria-pressed="${!!r.monitored}" title="${r.monitored?'Monitored':'Monitor release'}">${icon('monitor')}</button></div></div></article>`;
}
function syncPanel(sync={}) {return `<div class="row"><span class="status-dot" style="background:var(${sync.running?'--gold':sync.last_error?'--accent':'--teal'})"></span><h3>${sync.running?'Sync in progress':sync.last_error?'Sync needs attention':'Your collection, connected'}</h3></div><p>${e(sync.running?(sync.stage||'Fetching real metadata and release updates…'):sync.last_error||'Refresh your configured lists, actress metadata, and watched titles. No fabricated cast matches.')}</p><div class="progress-track ${sync.running?'running':''}" ${sync.running?'role="progressbar" aria-label="Sync in progress"':''}></div><div class="sync-meta"><span>${sync.running?'Working in the background':'Last completed sync'}</span><span>${sync.last_run?e(formatDate(sync.last_run,{month:'short',day:'numeric',hour:'2-digit',minute:'2-digit'})):'Not yet synced'}</span></div><div class="actions"><button class="btn small" data-action="sync" ${sync.running?'disabled':''}>${icon('refresh')}${sync.running?'Syncing…':'Sync now'}</button><a href="#/settings" class="text-button">Manage sources ${icon('arrow')}</a></div>`;}
function activityList(items,consoleMode=false) {if(!items.length)return `<p class="no-results-note">No activity yet. Your sync progress and integration results will appear here.</p>`;return `<div class="activity-list ${consoleMode?'console':''}">${items.map(a=>`<article class="activity-item" data-status="${e(a.status)}"><span class="status-dot"></span><div><small>${e(a.kind||'Activity')} · ${e(a.status||'Info')}</small><p>${e(a.message)}</p></div><time datetime="${e(a.created_at)}">${e(formatDate(a.created_at,{month:'short',day:'numeric',hour:'2-digit',minute:'2-digit'}))}</time></article>`).join('')}</div>`;}
function renderActivity(data,dashboard) {$('#main').innerHTML=heading('Behind the scenes.','Real progress, connection results, and every completed action.',`<div class="actions"><button class="btn" data-action="retry">${icon('refresh')}Refresh</button></div>`)+`<section class="panel sync-panel" id="activity-sync">${syncPanel(dashboard.sync)}</section><section class="panel"><div class="section-heading"><h2>Activity console</h2><span>${count(data.total)} events</span></div><div id="activity-console">${activityList(asArray(data.items),true)}</div></section>`;}
async function runSync(button) {await busy(button,async()=>{await api('/api/sync',{method:'POST',body:{}});toast('Sync started. Progress will appear in activity.');const dashboard=await getDashboard();updateLiveDashboard(dashboard);},'Starting…');}
function updateLiveDashboard(data) {for(const id of ['#overview-sync','#activity-sync']){const node=$(id);if(node)node.innerHTML=syncPanel(data.sync);}if($('#overview-activity'))$('#overview-activity').innerHTML=activityList(asArray(data.activity).slice(0,4));}
async function poll() {
  if(state.polling||document.hidden||!state.session?.authenticated||state.route==='settings')return;state.polling=true;
  try{const data=await getDashboard();updateLiveDashboard(data);if(state.route==='activity'){const activity=await api('/api/activity');if($('#activity-console'))$('#activity-console').innerHTML=activityList(asArray(activity.items),true);}}
  catch(error){const node=$('#sync-status');if(state.session?.authenticated){node.className='sync-chip error';node.innerHTML='<i></i><span>Offline</span>';node.title=error.message;}}finally{state.polling=false;}
}
function renderCalendarPage() {$('#main').innerHTML=heading('Mark the moment.','A release calendar built from dates actually supplied by your sources.')+`<div class="calendar-toolbar"><h2 id="calendar-month"></h2><button class="btn small" data-action="calendar-today">Today</button><button class="icon-button" data-action="calendar-prev" aria-label="Previous month">${icon('chevron','rotation-180')}</button><button class="icon-button" data-action="calendar-next" aria-label="Next month">${icon('chevron')}</button></div><div id="calendar-content">${loading('Loading the complete release calendar…')}</div><p class="field-help" id="calendar-note"></p>`;drawCalendarTitle();}
function drawCalendarTitle(){if($('#calendar-month'))$('#calendar-month').textContent=state.calendar.toLocaleDateString(undefined,{month:'long',year:'numeric'});}
async function loadCalendar() {
  const token=state.routeToken;const node=$('#calendar-content');if(!node)return;
  try{const releases=[];let page=1,total=Infinity;while(releases.length<total){const data=await api(`/api/releases?window=all&sort=release&page=${page}&limit=100`);if(token!==state.routeToken)return;total=Number(data.total);const batch=asArray(data.items);if(!batch.length&&releases.length<total)throw new Error('The server returned an incomplete calendar. Reload to try again.');releases.push(...batch);page++;if(!Number.isFinite(total)||page>10000)throw new Error('The server returned an invalid release count.');}const unique=[...new Map(releases.map(r=>[r.id,r])).values()];if(unique.length!==total)throw new Error('Release pages changed while loading. Refresh to see the complete calendar.');state.calendarItems=unique;remember(unique);drawCalendar();}
  catch(error){if(token===state.routeToken)node.innerHTML=errorView(error);}
}
function drawCalendar() {
  drawCalendarTitle();const items=state.calendarItems;const month=`${state.calendar.getFullYear()}-${String(state.calendar.getMonth()+1).padStart(2,'0')}`;const monthly=items.filter(r=>releaseDate(r).startsWith(month)).sort((a,b)=>releaseDate(a).localeCompare(releaseDate(b))||String(a.title).localeCompare(String(b.title)));const cells=calendarCells(state.calendar.getFullYear(),state.calendar.getMonth());const today=localDate();
  $('#calendar-content').innerHTML=`<div class="calendar-grid">${['Mon','Tue','Wed','Thu','Fri','Sat','Sun'].map(day=>`<div class="weekday">${day}</div>`).join('')}${cells.map(date=>`<div class="calendar-day ${date?'':'blank'} ${date===today?'today':''}">${date?`<span class="day-number">${Number(date.slice(-2))}${date===today?' · Today':''}</span>${monthly.filter(r=>releaseDate(r)===date).map(r=>`<button class="calendar-release ${releaseWindow(r)==='upcoming'?'upcoming':''}" data-action="release" data-id="${e(r.id)}" title="${e(r.title)}">${e(r.title)}<small>${e(mediaName(r.type))}</small></button>`).join('')}`:''}</div>`).join('')}</div><div class="calendar-list">${monthly.length?monthly.map(r=>`<article class="calendar-agenda-row"><span class="agenda-date"><b>${Number(releaseDate(r).slice(-2))}</b>${e(formatDate(releaseDate(r),{weekday:'short'}))}</span>${art(r.poster,'')}<div><button class="text-button" data-action="release" data-id="${e(r.id)}">${e(r.title)}</button><small>${e(mediaName(r.type))} · ${r.year||'Year unknown'}</small></div><span class="spacer"></span>${icon('chevron')}</article>`).join(''):empty('A clear calendar.','No supplied release dates fall in this month.',{name:'calendar'})}</div>`;
  const unknown=items.filter(r=>!releaseDate(r)).length;$('#calendar-note').textContent=`${monthly.length} releases this month. ${unknown} titles with unknown dates are available in All releases, not placed on the calendar. Dates are source metadata, not a promise of regional availability.`;
}
function renderActressesPage() {
  $('#main').innerHTML=heading('Follow the people.','Portraits, profiles, and every genuinely matched release.',`<div class="actions"><button class="btn" data-action="import-actresses">${icon('upload')}Import</button><button class="btn primary" data-action="add-actress">${icon('plus')}Add watch</button></div>`)+`<div class="toolbar"><label class="search-field">${icon('search')}<span class="visually-hidden">Search watched actresses</span><input id="actress-query" type="search" placeholder="Search your watched actresses…" value="${e(state.actressQuery)}"></label>${groupSelect('actress-group',state.actressGroup)}<label class="check"><input type="checkbox" id="actress-upcoming-only" ${state.actressUpcomingOnly?'checked':''}>Upcoming releases only</label></div><div class="result-summary"><span id="actress-total"></span><span>Source-linked matches only</span></div><div id="actress-grid" class="actress-grid">${loading('Loading actress watches…')}</div><p class="field-help">MDBList is the primary metadata source. Optional TMDb fallback fills missing person information and supplies verified cast / credits. Historical discovery is bounded and cached; it never triggers automatic downloads. Releases appear only when a source establishes an identity-linked match; coverage may be incomplete.</p>`;
  const search=debounce(input=>{if(input.isConnected)loadActresses();});
  $('#actress-query').addEventListener('input',event=>{state.actressQuery=event.target.value;state.actressToken++;search(event.target);});$('#actress-group').addEventListener('change',event=>{state.actressGroup=event.target.value;loadActresses();});
  $('#actress-upcoming-only').addEventListener('change',event=>{state.actressUpcomingOnly=event.target.checked;loadActresses();});
}
async function loadActresses() {
  const token=++state.actressToken;const routeToken=state.routeToken;const grid=$('#actress-grid');if(!grid)return;grid.setAttribute('aria-busy','true');
  const query=new URLSearchParams({q:state.actressQuery,group:state.actressGroup});if(state.actressUpcomingOnly)query.set('upcoming_only','true');
  try{const data=await api('/api/actresses?'+query);if(token!==state.actressToken||routeToken!==state.routeToken)return;const items=asArray(data.items);if(!state.actressQuery&&!state.actressGroup&&!state.actressUpcomingOnly)state.actresses=items;$('#actress-total').textContent=`${count(data.total)} actress watches`;grid.innerHTML=items.length?items.map(actressCard).join(''):state.actressUpcomingOnly?empty('No watches have upcoming releases.','No source-linked upcoming releases match these filters. Unknown dates and incomplete source coverage are not proof that no future titles exist.',{name:'calendar'}):empty('Your people, in focus.','Add an actress using her TMDb person ID or import your watch list. Biographies and real matched releases will appear here.',{name:'users',actions:`<button class="btn primary" data-action="add-actress">${icon('plus')}Add actress watch</button>`});}
  catch(error){if(token===state.actressToken&&routeToken===state.routeToken)grid.innerHTML=errorView(error);}finally{if(grid.isConnected)grid.removeAttribute('aria-busy');}
}
function actressCard(a) {return `<article class="actress-card"><div class="actress-portrait">${art(a.profile,'','user')}<button class="portrait-link" data-action="actress" data-id="${e(a.id)}" aria-label="View ${e(a.name)} and all matched releases"></button>${a.group?`<span class="badge">${e(a.group)}</span>`:''}</div><div class="actress-body"><button class="card-title-button" data-action="actress" data-id="${e(a.id)}"><h3>${e(a.name||'Unnamed actress')}</h3></button><p>${e(a.biography||'Biography not supplied by your metadata source. Open this watch to view its identity and matching status.')}</p><div class="actress-counts"><span><b>${count(a.release_count)}</b>releases</span><span><b>${count(a.upcoming_count)}</b>upcoming</span></div><div class="actress-status"><span class="status-dot" style="background:var(${a.monitored?'--teal':'--quiet'})"></span>${a.monitored?'Watching':'Watch paused'}<span class="spacer"></span>${a.auto_add?'<span class="badge red">Auto-add</span>':''}${a.notify?`<span title="Notifications enabled">${icon('bell')}</span>`:''}<button class="text-button" data-action="actress" data-id="${e(a.id)}" aria-label="Open ${e(a.name)}">${icon('arrow')}</button></div></div></article>`;}
async function openActress(id) {
  const dialog=makeDialog('Actress watch',loading('Loading profile and all matched releases…'),{wide:true});
  try{const data=await api(`/api/actresses/${encodeURIComponent(id)}`);if(!dialog.open)return;const a=data.actress;const releases=asArray(data.releases);remember(releases);
    $('.dialog-body',dialog).innerHTML=`<div class="actress-detail-heading">${art(a.profile,a.name,'user')}<div><div class="row wrap"><span class="eyebrow" style="margin-bottom:0">ACTRESS WATCH</span>${a.group?`<span class="badge">${e(a.group)}</span>`:''}</div><h2>${e(a.name)}</h2><div class="row wrap"><span class="badge ${a.monitored?'teal':''}">${a.monitored?'Watching':'Paused'}</span><span class="badge">${count(data.total)} matched releases</span><span class="badge gold">${count(a.upcoming_count)} upcoming</span></div><dl class="detail-facts">${birthdayFact(a)}<div><dt>Place of birth</dt><dd>${e(a.place_of_birth||'Not supplied')}</dd></div><div><dt>TMDb person ID</dt><dd>${e(a.tmdb_id||'Not supplied')}</dd></div><div><dt>MDBList biography status</dt><dd>${e(currentPersonMetadataStatus(a.metadata_status))}</dd></div></dl></div></div><p class="biography">${e(a.biography||'A biography is not available from the configured metadata sources. The supplied identity is preserved; no profile has been invented.')}</p><div class="actions"><button class="btn small" data-local="edit">${icon('settings')}Edit watch</button><button class="btn small primary" data-local="bulk" ${releases.length&&Number(data.total)===releases.length?'':'disabled'}>${icon('plus')}Add matched releases</button><button class="btn small danger" data-local="delete">${icon('trash')}Remove watch</button></div>${personDiscoveryCoverage()}${Number(data.total)!==releases.length?`<div class="notice error">The API reported ${count(data.total)} matches but returned ${releases.length}. This is an incomplete response; refresh before using bulk acquisition.</div>`:''}<div class="section-heading"><h2>All matched releases</h2><span>${count(releases.length)} returned</span></div><div class="cover-grid">${releases.length?releases.map(releaseCard).join(''):empty('No matches to show yet.','Discovery coverage is incomplete. Zero known matches is not proof that this person has no releases.',{name:'film'})}</div>`;
    $('[data-local=edit]',dialog).addEventListener('click',()=>editActress(a,()=>{dialog.close();openActress(id);}));$('[data-local=bulk]',dialog).addEventListener('click',()=>bulkAcquire(a,releases));$('[data-local=delete]',dialog).addEventListener('click',()=>deleteActress(a,dialog));
  }catch(error){if(dialog.open){$('.dialog-body',dialog).innerHTML=empty('Couldn’t load this actress watch',error.message,{name:'info',error:true,actions:'<button class="btn" data-retry-actress>Try again</button>'});$('[data-retry-actress]',dialog).addEventListener('click',()=>{dialog.close();openActress(id);},{once:true});}}
}
function watchFields(a={monitored:true,auto_add:false,notify:false}) {return `<label>Group<input name="group" value="${e(a.group||'')}" placeholder="Optional group name" maxlength="120"></label><label class="check"><input type="checkbox" name="monitored" ${a.monitored?'checked':''}>Monitor this actress</label><label class="check"><input type="checkbox" name="auto_add" ${a.auto_add?'checked':''}>Automatically add future matched releases</label><label class="check"><input type="checkbox" name="notify" ${a.notify?'checked':''}>Notify on new matched releases</label><p class="field-help">Auto-add and notifications also require the corresponding global settings. Existing releases do not silently trigger a notification flood.</p>`;}
function readWatch(form) {const data=new FormData(form);return {group:String(data.get('group')||''),monitored:data.has('monitored'),auto_add:data.has('auto_add'),notify:data.has('notify')};}
function editActress(a=null,after=null) {
  const dialog=makeDialog(a?'Edit actress watch':'Add an actress watch',`<form id="watch-form">${!a?`<div class="form-grid"><label class="span2">TMDb person ID or MDBList actor URL<input name="identity" required autofocus placeholder="Decimal person ID or https://mdblist.com/actor/…" autocomplete="off"><small>MDBList is the primary metadata source. Name-only lookup is not supported.</small></label><label class="span2">Fallback source name<input name="name" placeholder="Preserve the known name if metadata is unavailable" maxlength="300" required></label></div>`:''}<div style="margin-top:15px">${watchFields(a||undefined)}</div><p class="form-error" role="alert" hidden></p><div class="dialog-actions"><button type="button" class="btn" data-cancel>Cancel</button><button class="btn primary" type="submit">${a?'Save watch':'Add watch'}</button></div></form>`,{narrow:true});
  $('[data-cancel]',dialog).addEventListener('click',()=>dialog.close());$('#watch-form',dialog).addEventListener('submit',async event=>{event.preventDefault();const form=event.currentTarget;$('.form-error',dialog).hidden=true;await busy($('[type=submit]',form),async()=>{try{const body=readWatch(form);if(!a){body.tmdb_id=parsePersonIdentity(form.elements.identity.value);body.name=form.elements.name.value;}const result=await api(a?`/api/actresses/${a.id}`:'/api/actresses',{method:a?'PATCH':'POST',body});const verify=a?await api(`/api/actresses/${a.id}`):await api('/api/actresses');if(a){if(verify.actress.group!==body.group||verify.actress.monitored!==body.monitored||verify.actress.auto_add!==body.auto_add||verify.actress.notify!==body.notify)throw new Error('The server did not preserve all watch settings. Refresh and check this watch.');}else if(!asArray(verify.items).some(item=>item.tmdb_id===body.tmdb_id))throw new Error('The new watch was not found on readback. Refresh before retrying.');toast(result.warning||'Actress watch saved.');dialog.close();await refreshActressCache();if(after)after();else if(state.route==='actresses'){renderActressesPage();await loadActresses();}}catch(error){dialogError(dialog,error);}},'Saving…');});
}
async function refreshActressCache(){const result=await api('/api/actresses');state.actresses=asArray(result.items);}
function importActresses() {
  const dialog=makeDialog('Import actress watches',`<p class="no-results-note">Paste a JSON array of source entries, or an object with <code>entries</code> and optional <code>groups</code>. Each entry needs <code>name</code>, a literal string <code>actor_id</code>, and optional <code>group</code>. IDs are preserved exactly and duplicates are handled on the server.</p><form id="import-form"><label>Watch list JSON<textarea name="entries" required autofocus spellcheck="false" placeholder="Paste your exported watch list here. No sample roster is included."></textarea></label><label style="margin-top:12px">Or load a local JSON file<input type="file" id="import-file" accept="application/json,.json"></label><p class="field-help">Only your explicitly submitted watch list is imported. This does not modify MDBList favorites. Duplicate literal IDs are deduplicated by the backend, not rewritten here.</p><fieldset class="import-defaults"><legend>Defaults for new entries</legend>${watchFields()}<p class="field-help">These controls use the same fields as Add watch and are sent as top-level group, monitored, auto_add, and notify defaults. Explicit entry flags and entry groups win; original groups mappings are preserved. Defaults do not replace existing watch settings unless an entry explicitly changes them.</p></fieldset><p class="form-error" role="alert" hidden></p><div class="dialog-actions"><button type="button" class="btn" data-cancel>Cancel</button><button type="submit" class="btn primary">${icon('upload')}Import watches</button></div></form>`,{narrow:true});
  $('[data-cancel]',dialog).addEventListener('click',()=>dialog.close());$('#import-file',dialog).addEventListener('change',async event=>{const file=event.target.files[0];if(!file)return;if(file.size>2*1024*1024){dialogError(dialog,new Error('Choose a JSON file smaller than 2 MB.'));return;}try{$('textarea',dialog).value=await file.text();}catch(error){dialogError(dialog,error);}});
  $('#import-form',dialog).addEventListener('submit',async event=>{event.preventDefault();const form=event.currentTarget;try{const parsed=JSON.parse(form.elements.entries.value);if(!parsed||typeof parsed!=='object')throw new Error('Provide an entries array or an object containing entries.');const payload={...(Array.isArray(parsed)?{entries:parsed}:parsed),...readWatch(form)};if(!Array.isArray(payload.entries)||!payload.entries.length)throw new Error('Provide a nonempty entries array.');for(const [index,entry] of payload.entries.entries()){if(typeof entry.name!=='string'||!entry.name||typeof entry.actor_id!=='string'||!/^\d+$/.test(entry.actor_id))throw new Error(`Entry ${index+1} needs a name and an unchanged decimal actor_id string. Numeric JSON IDs cannot preserve source tokens.`);}await busy($('[type=submit]',form),async()=>{await api('/api/actresses/import',{method:'POST',body:payload,timeout:120000});await refreshActressCache();const missing=payload.entries.filter(entry=>!state.actresses.some(a=>a.tmdb_id===entry.actor_id));if(missing.length)throw new Error(`${missing.length} imported identities were not found on readback. Check activity before retrying.`);toast('Watch list imported. Metadata enrichment is queued.');dialog.close();if(state.route==='actresses'){renderActressesPage();await loadActresses();}},'Importing…');}catch(error){dialogError(dialog,error);}});
}
function jsonDetails(title, value, unavailable='Not supplied by the metadata source.') {
  const supplied=value!==null&&value!==undefined&&(typeof value!=='object'||Object.keys(value).length>0);
  return `<details class="details-block"><summary>${e(title)}</summary>${supplied?`<pre class="detail-json">${e(JSON.stringify(value,null,2))}</pre>`:`<p class="field-help">${e(unavailable)}</p>`}</details>`;
}
function calendarDateParts(value) {
  if(typeof value!=='string'||!/^\d{4}-\d{2}-\d{2}$/.test(value))return null;
  const [year,month,day]=value.split('-').map(Number);
  const leap=year%4===0&&(year%100!==0||year%400===0);
  const days=[31,leap?29:28,31,30,31,30,31,31,30,31,30,31];
  return year>=1&&month>=1&&month<=12&&day>=1&&day<=days[month-1]?[year,month,day]:null;
}
export function ageYears(birthday,todayUTC=new Date().toISOString().slice(0,10)) {
  const birth=calendarDateParts(birthday),today=calendarDateParts(todayUTC);
  if(!birth||!today||birthday>todayUTC)return null;
  return today[0]-birth[0]-(today[1]<birth[1]||(today[1]===birth[1]&&today[2]<birth[2])?1:0);
}
function birthdayFact(person) {
  const age=person.deathday?ageYears(person.birthday,person.deathday):ageYears(person.birthday);
  return `<div><dt>Birthday</dt><dd class="birthday-fact"><span>${e(calendarDateParts(person.birthday)?formatDate(person.birthday):'Not supplied')}</span>${age!==null?`<span class="badge">${age} ${person.deathday?'years at death':'years old'}</span>`:''}</dd></div>`;
}
function detailFact(label,value) {return `<div><dt>${e(label)}</dt><dd>${e(value||'Not supplied')}</dd></div>`;}
function watchlistMarkup(id,data={status:'unknown'}) {
  const present=data.status==='present'&&data.in_watchlist===true,absent=data.status==='absent'&&data.in_watchlist===false;
  const label=present?'In MDBList watchlist':absent?'Not in MDBList watchlist':'MDBList watchlist status unknown';
  return `<span class="badge ${present?'teal':''}" role="status">${label}</span>${data.error?`<p class="form-error" role="alert">${e(data.error)}</p>`:''}<div class="actions">${present||absent?`<button class="btn small" data-action="watchlist-set" data-id="${e(id)}" data-present="${!present}">${present?'Remove from':'Add to'} MDBList watchlist</button>`:''}<button class="btn small" data-action="watchlist-retry" data-id="${e(id)}">${present||absent?'Refresh watchlist status':'Retry watchlist status'}</button></div>`;
}
function checkedWatchlist(data) {
  if(data?.status==='present'&&data.in_watchlist===true||data?.status==='absent'&&data.in_watchlist===false)return data;
  return {status:'unknown',error:data?.error||'The provider did not return a known watchlist status.'};
}
async function loadWatchlist(dialog,id) {
  const node=$('[data-watchlist]',dialog);if(!node||!dialog.open)return;const token=dialog.watchlistToken=(dialog.watchlistToken||0)+1;
  node.innerHTML=loading('Checking MDBList watchlist…');
  try{const data=checkedWatchlist(await api(`/api/releases/${encodeURIComponent(id)}/watchlist`));if(node.isConnected&&dialog.open&&token===dialog.watchlistToken)node.innerHTML=watchlistMarkup(id,data);}
  catch(error){if(node.isConnected&&dialog.open&&token===dialog.watchlistToken)node.innerHTML=watchlistMarkup(id,{status:'unknown',error:error.message});}
}
async function setWatchlist(id,button) {
  const dialog=button.closest('dialog'),present=button.dataset.present==='true';
  await busy(button,async()=>{
    if(!await confirmAction(present?'Add to MDBList watchlist?':'Remove from MDBList watchlist?','This changes your external MDBList account watchlist. It does not monitor, acquire, or download this release.',present?'Confirm watchlist add':'Confirm watchlist removal'))return;
    if(!dialog?.open)return;lockDialog(dialog,true);dialog.watchlistToken=(dialog.watchlistToken||0)+1;
    try{
      const result=await api(`/api/releases/${encodeURIComponent(id)}/watchlist`,{method:'POST',body:{in_watchlist:present},timeout:120000});
      if(result.verified!==true)throw new Error('The server did not verify the watchlist change. Retry the status check before taking another action.');
      const current=checkedWatchlist(await api(`/api/releases/${encodeURIComponent(id)}/watchlist`));
      if(current.status!== (present?'present':'absent')||current.in_watchlist!==present)throw new Error('Watchlist readback did not match the requested change. Retry the status check before taking another action.');
      const node=$('[data-watchlist]',dialog);if(node?.isConnected)node.innerHTML=watchlistMarkup(id,current);toast('MDBList watchlist change verified.');
    }catch(error){const node=$('[data-watchlist]',dialog);if(node?.isConnected)node.innerHTML=watchlistMarkup(id,{status:'unknown',error:error.message});}
    finally{lockDialog(dialog,false);}
  },'Confirming…');
}
function releaseDetailMarkup(r) {
  const target=acquisitionTarget(r);const links=asArray(r.source_links).map(safeExternal).filter(Boolean);
  const imdb=/^tt\d+$/.test(r.imdb_id||'')?`https://www.imdb.com/title/${r.imdb_id}/`:'';
  return `<div class="detail-layout">${art(r.poster,r.title,'film','detail-poster')}<div><span class="eyebrow">${e(r.source||'MDBList metadata')}</span><h2>${e(r.title||'Untitled release')}</h2><div class="row wrap"><span class="badge">${e(mediaName(r.type))}</span><span class="badge">${e(r.year||'Year unknown')}</span>${r.score!==null&&r.score!==undefined?`<span class="badge gold">${e(r.score)} MDBList score</span>`:''}${r.monitored?'<span class="badge teal">Monitored</span>':''}</div>${releaseSourceMarkup(r)}${arrPresenceMarkup(r)}${availabilityMarkup(r)}<div class="content-advice"><span class="badge">Certification: ${e(r.certification||'Not supplied')}</span><span class="badge">Suggested age: ${r.age_rating!==null&&r.age_rating!==undefined?e(r.age_rating)+'+':'Not supplied'}</span></div><p class="detail-description">${e(r.description||'A description has not been supplied. No summary has been invented.')}</p><dl class="detail-facts">${detailFact('Release date',r.release_date?formatDate(r.release_date):'Unknown')}${detailFact('Digital date',r.digital_date?formatDate(r.digital_date):'Unknown')}${detailFact('Runtime',r.runtime?`${r.runtime} minutes`:'Unknown')}${detailFact('Country',r.country)}${detailFact('Status',r.status)}${detailFact('Genres',asArray(r.genres).join(' / '))}${detailFact('IMDb ID',r.imdb_id)}${detailFact('TMDb ID',r.tmdb_id)}${detailFact('TVDb ID',r.tvdb_id)}${detailFact('MDBList ID',r.mdblist_id)}</dl><div class="actions">${target?`<button class="btn primary" data-action="acquire" data-id="${e(r.id)}">${icon(hasAcquisition(r,target)?'check':'plus')}${hasAcquisition(r,target)?'Monitor & search in '+targetName(target):'Add to '+targetName(target)}</button>`:''}<button class="btn ${r.monitored?'on':''}" data-action="monitor" data-id="${e(r.id)}" aria-pressed="${!!r.monitored}">${icon('monitor')}${r.monitored?'Stop monitoring':'Monitor release'}</button></div></div></div><section class="panel watchlist-panel"><h3>MDBList watchlist</h3><p class="field-help">Separate from personal-list membership, local monitoring, and acquisition. Unknown never means absent. Only a confirmed action changes your account.</p><div data-watchlist>${loading('Checking MDBList watchlist…')}</div></section><section class="panel cast-panel"><h3>Release cast</h3><div data-release-cast>${loading('Loading supplied cast…')}</div></section><section class="panel" style="margin-top:22px"><h3>Matched actress watches</h3>${asArray(r.actresses).length?`<div class="actions">${r.actresses.map(a=>`<button class="btn small" data-action="actress" data-id="${e(a.id)}">${icon('user')}${e(a.name)}${a.group?' · '+e(a.group):''}</button>`).join('')}</div>`:'<p class="field-help">No identity-linked actress matches were supplied. This is not a claim that the cast is empty.</p>'}<p class="field-help">${e(r.coverage||'Coverage is unknown and may be incomplete. MDBList biographies are not filmographies; no cast matches are inferred.')}</p></section><div class="notice info">Certification and suggested age are separate source fields. Parental numbers or keywords do not establish nudity or particular scenes. Provider metadata is not a guarantee of availability in your region.</div>${jsonDetails('Content advisories — original source fields',r.content_info)}${jsonDetails('Ratings — original source values',r.ratings)}${jsonDetails('Watch providers — region availability unverified',r.watch_providers)}${jsonDetails('Next episode — source metadata',r.next_episode)}${jsonDetails('Raw MDBList metadata',r.raw)}${jsonDetails('Local acquisition state',r.acquisition)}<div class="source-links"><a href="https://mdblist.com/" target="_blank" rel="noopener noreferrer">Metadata: MDBList</a>${imdb?`<a href="${e(imdb)}" target="_blank" rel="noopener noreferrer">IMDb title</a>`:''}${links.map((href,i)=>`<a href="${e(href)}" target="_blank" rel="noopener noreferrer">${/^https:\/\/(?:www\.)?mdblist\.com\/(?:movie|show)\/[^/?#]+/.test(href)?'MDBList title':'Source '+(i+1)}</a>`).join('')}</div><p class="field-help">Updated ${e(formatDate(r.updated_at))}. Missing fields are shown as unknown, never fabricated.</p>`;
}
async function openRelease(id) {
  const dialog=makeDialog('Release details',loading('Loading the original metadata…'),{wide:true});dialog.dataset.releaseId=String(id);
  const load=async()=>{try{const r=await api(`/api/releases/${encodeURIComponent(id)}`);if(!dialog.open)return;remember([r]);$('.dialog-body',dialog).innerHTML=releaseDetailMarkup(r);await Promise.all([loadReleaseCast(dialog,id),loadWatchlist(dialog,id)]);}catch(error){if(dialog.open){$('.dialog-body',dialog).innerHTML=empty('Couldn’t load this release',error.message,{name:'info',error:true,actions:'<button class="btn" data-retry-detail>Try again</button>'});$('[data-retry-detail]',dialog).addEventListener('click',()=>{$('.dialog-body',dialog).innerHTML=loading();load();},{once:true});}}};
  await load();return dialog;
}
function refreshReleaseUI(r) {
  remember([r]);const active=document.activeElement;const activeAction=active?.dataset.action;let replacementFocus=null;
  $$('.release-card').forEach(card=>{if($('[data-id]',card)?.dataset.id!==String(r.id))return;const hadFocus=card.contains(active);const template=document.createElement('template');template.innerHTML=releaseCard(r);const replacement=template.content.firstElementChild;card.replaceWith(replacement);if(hadFocus)replacementFocus=$(`[data-action="${activeAction}"]`,replacement);});
  $$('dialog[data-release-id]').forEach(dialog=>{if(dialog.dataset.releaseId!==String(r.id))return;const hadFocus=dialog.contains(active);$('.dialog-body',dialog).innerHTML=releaseDetailMarkup(r);loadReleaseCast(dialog,r.id);loadWatchlist(dialog,r.id);if(hadFocus)replacementFocus=$(`[data-action="${activeAction}"]`,dialog);});
  replacementFocus?.focus({preventScroll:true});
}
async function monitorRelease(id,button) {
  await busy(button,async()=>{const current=state.releases.get(String(id))||await api(`/api/releases/${encodeURIComponent(id)}`);const monitored=!current.monitored;await api(`/api/releases/${encodeURIComponent(id)}`,{method:'PATCH',body:{monitored}});const verified=await api(`/api/releases/${encodeURIComponent(id)}`);if(verified.monitored!==monitored)throw new Error('The monitoring change was not preserved. Refresh before retrying.');refreshReleaseUI(verified);toast(monitored?'Release monitoring enabled.':'Release monitoring paused.');});
}
function lockDialog(dialog,locked) {
  dialog.dataset.pending=String(locked);
  $$('button,input,select,textarea',dialog).forEach(control=>{
    if(locked){if(!('pendingDisabled' in control.dataset))control.dataset.pendingDisabled=String(control.disabled);control.disabled=true;}
    else if('pendingDisabled' in control.dataset){control.disabled=control.dataset.pendingDisabled==='true';delete control.dataset.pendingDisabled;}
  });
}
function acquisitionOutcome(result) {
  return `${result.monitored_restored===true?'Monitoring restored.':'Monitoring restoration not confirmed.'} ${result.search_started===true?'Search started.':'Search start not confirmed. Check Activity before retrying.'}`;
}
async function acquireRelease(id) {
  const dialog=makeDialog('Prepare acquisition',loading('Loading saved integration settings…'),{narrow:true});
  try {
    const [r,settings]=await Promise.all([api(`/api/releases/${encodeURIComponent(id)}`),api('/api/settings')]);if(!dialog.open)return;remember([r]);const target=acquisitionTarget(r);if(!target)throw new Error('This release has no compatible media type. It cannot be sent to an integration.');
    const config=settings[target]||{};const title=$('.dialog-header h2',dialog);title.textContent=hasAcquisition(r,target)?`Monitor & search in ${targetName(target)}?`:`Add to ${targetName(target)}?`;
    if(!config.configured||!config.enabled){$('.dialog-body',dialog).innerHTML=empty(`${targetName(target)} is not ready`,!config.configured?'Configure and save its server URL and API key first.':'Enable this integration in Settings before adding releases.',{name:'settings',actions:'<a href="#/settings" class="btn primary">Open settings</a>'});return dialog;}
    let options={};let warning='';try{options=await api(`/api/integrations/${target}/options`);}catch(error){warning=error.message;}
    if(!dialog.open)return;
    $('.dialog-body',dialog).innerHTML=`<p class="no-results-note">Send <strong>${e(r.title)}</strong> (${e(mediaName(r.type))}) to <strong>${targetName(target)}</strong>? This changes your external library.</p><p class="no-results-note">Existing entries will be set to monitored, including all Sonarr seasons. New entries will also be monitored. A search will start and may download media.</p>${warning?`<p class="notice error">Could not load current options: ${e(warning)}. Saved values remain available; the server will validate them.</p>`:''}<form id="acquire-form"><fieldset class="settings-fields"><div class="form-grid"><label class="span2">Quality profile<select required name="quality_profile_id">${optionMarkup(options.quality_profiles,config.quality_profile_id||'','id','name','Select quality profile')}</select></label><label class="span2">Root folder<select required name="root_folder">${optionMarkup(options.root_folders,config.root_folder,'path','path','Select root folder')}</select></label></div><label class="check" style="margin-top:16px"><input type="checkbox" name="search" checked disabled>Start a search (may download media)</label><p class="field-help">This explicit manual action always monitors and searches, independently of automatic-add settings.</p></fieldset><p class="form-error" role="alert" hidden></p><div class="dialog-actions"><button type="button" class="btn" data-cancel>Cancel</button><button type="submit" class="btn primary">${hasAcquisition(r,target)?'Confirm monitor & search':'Confirm add'}</button></div></form>`;
    $('[data-cancel]',dialog).addEventListener('click',()=>dialog.close());$('#acquire-form',dialog).addEventListener('submit',async event=>{
      event.preventDefault();const form=event.currentTarget;if(dialog.dataset.pending==='true')return;const data=new FormData(form);const body={target,search:true,quality_profile_id:Number(data.get('quality_profile_id')),root_folder:String(data.get('root_folder')||'')};
      if(!Number.isSafeInteger(body.quality_profile_id)||body.quality_profile_id<1||!body.root_folder){dialogError(dialog,new Error('Choose a valid quality profile and root folder.'));return;}
      $('.form-error',dialog).hidden=true;
      await busy($('[type=submit]',form),async()=>{lockDialog(dialog,true);try {
        const result=await api(`/api/releases/${encodeURIComponent(id)}/acquire`,{method:'POST',body,timeout:120000});if(result.ok!==true||!result.result)throw new Error('The server did not confirm the acquisition. Check Activity before retrying.');
        const verified=await api(`/api/releases/${encodeURIComponent(id)}`);if(!verified.acquisition?.[target])throw new Error('The external operation returned, but local readback has no acquisition state. Check Activity before retrying.');refreshReleaseUI(verified);
        const href=safeExternal(result.result.url);$('.dialog-body',dialog).innerHTML=`<div class="notice info" role="status">${result.result.already_exists?'Already present and verified in '+targetName(target)+'.':'Added and verified in '+targetName(target)+'.'}</div><p class="field-help">External ID: ${e(result.result.id)}. </p><p class="field-help">${e(acquisitionOutcome(result.result))}</p><div class="dialog-actions">${href?`<a class="btn" href="${e(href)}" target="_blank" rel="noopener noreferrer">Open ${targetName(target)} ${icon('arrow')}</a>`:''}<button class="btn primary" data-done>Done</button></div>`;$('[data-done]',dialog).addEventListener('click',()=>dialog.close());$('[data-done]',dialog).focus();toast(`${r.title}: ${acquisitionOutcome(result.result)}`);
      }catch(error){dialogError(dialog,error);}finally{lockDialog(dialog,false);}},'Adding…');
    });
  }catch(error){if(dialog.open){$('.dialog-body',dialog).innerHTML=empty('Couldn’t prepare acquisition',error.message,{name:'info',error:true,actions:'<button class="btn" data-retry-acquire>Try again</button>'});$('[data-retry-acquire]',dialog).addEventListener('click',()=>{dialog.close();acquireRelease(id);},{once:true});}}
  return dialog;
}
export function bulkScope(releases,scope,today=new Date().toISOString().slice(0,10)) {
  if(!['all','released','upcoming'].includes(scope))throw new Error('Unknown acquisition scope.');
  // The bulk API uses UTC release_date, not an inferred digital date.
  return asArray(releases).filter(r=>{const window=releaseWindow({...r,digital_date:''},today);return scope==='all'||(scope==='upcoming'?window==='upcoming':window==='current'||window==='past');});
}
export function summarizeBulkResult(data) {
  if(!Array.isArray(data.results)||!['added','failed','skipped'].every(key=>Number.isSafeInteger(data[key])&&data[key]>=0))throw new Error('Invalid bulk outcome response. Check Activity before retrying.');
  const ids=new Set();let added=0,failed=0,existing=0;
  for(const row of data.results){if(!row||!/^\d+$/.test(String(row.release_id))||ids.has(String(row.release_id))||typeof row.ok!=='boolean')throw new Error('Invalid or duplicate per-item bulk outcome. Check Activity before retrying.');ids.add(String(row.release_id));if(!row.ok)failed++;else if(row.already_exists)existing++;else added++;}
  if(added!==data.added||failed!==data.failed||data.skipped<existing)throw new Error('Bulk outcome totals do not match the per-item results. Check Activity before retrying.');
  return {results:data.results,added,failed,existing,excluded:data.skipped-existing,total:added+failed+data.skipped};
}
function bulkAcquire(a,releases) {
  const dialog=makeDialog('Add matched releases?',`<p class="no-results-note">Add actual matched releases for <strong>${e(a.name)}</strong> to your external libraries? Movies go to Radarr; series go to Sonarr. This is not a complete filmography. Only this confirmed scope is requested, using each integration’s saved profile and root folder.</p><form id="bulk-form"><fieldset class="settings-fields"><label>Release scope<select name="scope"><option value="all">All matched releases (including unknown dates)</option><option value="released">Released only (known date through today)</option><option value="upcoming">Upcoming only</option></select></label><label class="check" style="margin-top:15px"><input type="checkbox" name="search" checked disabled>Start searches (may download media)</label><p class="field-help">Existing entries will be set to monitored, including all Sonarr seasons. New entries will also be monitored. Searches will start and may download media. This manual action always monitors and searches. Scope dates use the server’s UTC calendar; unknown release dates are included only in All. Integration failures are reported per item, not hidden.</p><div id="bulk-preview" aria-live="polite"></div></fieldset><p class="form-error" hidden role="alert"></p><div class="dialog-actions"><button type="button" class="btn" data-cancel>Cancel</button><button type="submit" class="btn primary">Confirm bulk add</button></div></form>`,{wide:true});
  const form=$('#bulk-form',dialog);
  const preview=()=>{const selected=bulkScope(releases,form.elements.scope.value);$('#bulk-preview',dialog).innerHTML=`<p class="result-summary">${selected.length} matched release${selected.length===1?'':'s'} selected</p><div class="bulk-results">${selected.map(r=>`<div class="bulk-result">${icon('film')}<p>${e(r.title)}</p><span>${acquisitionTarget(r)?targetName(acquisitionTarget(r)):'Unsupported type'}</span></div>`).join('')}</div>`;$('[type=submit]',form).disabled=selected.length===0;};
  form.elements.scope.addEventListener('change',preview);preview();$('[data-cancel]',dialog).addEventListener('click',()=>dialog.close());
  form.addEventListener('submit',async event=>{
    event.preventDefault();if(dialog.dataset.pending==='true')return;const body={scope:form.elements.scope.value,search:true};if(!bulkScope(releases,body.scope).length)return;
    $('.form-error',dialog).hidden=true;
    await busy($('[type=submit]',form),async()=>{lockDialog(dialog,true);try{
      const response=await api(`/api/actresses/${encodeURIComponent(a.id)}/acquire`,{method:'POST',body,timeout:300000});const summary=summarizeBulkResult(response);let readbackError='';
      try {const current=await api(`/api/actresses/${encodeURIComponent(a.id)}`);const items=asArray(current.releases);const byID=new Map(items.map(r=>[String(r.id),r]));for(const row of summary.results.filter(row=>row.ok)){const r=byID.get(String(row.release_id));const target=r&&acquisitionTarget(r);if(!r||!target||!r.acquisition?.[target])throw new Error('At least one successful acquisition was absent from local readback. Review Activity before retrying.');refreshReleaseUI(r);}}
      catch(error){readbackError=error.message;}
      $('.dialog-body',dialog).innerHTML=`<div class="bulk-summary ${summary.failed||readbackError?'has-failures':''}" role="status"><span>${summary.added} added</span><span>${summary.existing} already present</span><span>${summary.failed} failed</span><span>${summary.excluded} excluded by scope</span></div><p class="field-help">${summary.results.length} per-item outcomes returned for ${summary.total} currently matched releases. Existing-item counts retain API compatibility; already present does not mean skipped. Monitoring and search outcomes are shown per item.</p>${summary.total!==releases.length?'<p class="notice info">The matched set changed during this request. These are the server’s actual outcomes, not the earlier preview.</p>':''}${readbackError?`<p class="notice error" role="alert">Server-reported outcomes below; local verification could not complete: ${e(readbackError)}</p>`:'<p class="field-help">Local acquisition readback verified.</p>'}<div class="bulk-results">${summary.results.map(row=>`<article class="bulk-result ${row.ok?'':'failed'}">${icon(row.ok?'check':'info')}<p>${e(row.title||'Untitled release')}<small>Local release ID ${e(row.release_id)}${row.error?' · '+e(row.error):''}</small>${row.ok?`<small>${e(acquisitionOutcome(row))}</small>`:''}</p><span>${row.ok?(row.already_exists?'Already present':'Added'):'Failed'}</span></article>`).join('')}</div>${summary.failed?'<p class="notice error">Some releases could not be added. Review each error above, correct the saved integration settings, then retry explicitly. Existing titles are checked idempotently.</p>':''}<div class="dialog-actions"><button class="btn primary" data-done>Done</button><a class="btn" href="#/activity">View activity</a></div>`;
      $('[data-done]',dialog).addEventListener('click',()=>dialog.close());$('[data-done]',dialog).focus();if(summary.failed||readbackError)toast('Bulk action finished with issues. Review the per-item outcomes.',true);else toast('Bulk action completed. Every returned outcome is shown.');
    }catch(error){dialogError(dialog,error);}finally{lockDialog(dialog,false);}},'Adding matched releases…');
  });
  return dialog;
}
function openSearch() {
  const dialog=makeDialog('Find a title',`<p class="no-results-note">Search the real MDBList catalogue. Adding a title saves its metadata locally; it does not acquire media or modify MDBList favorites.</p><form class="search-form"><label class="visually-hidden" for="catalogue-query">Title search</label><input id="catalogue-query" type="search" name="q" required maxlength="200" autofocus placeholder="Search movies and series…"><select name="type" aria-label="Catalogue media type"><option value="any">All types</option><option value="movie">Movies</option><option value="show">Series</option></select><button class="btn primary" type="submit">${icon('search')}Search</button></form><div class="search-results" aria-live="polite"><p class="field-help">Enter a title to search. No sample or suggested catalogue is preloaded.</p></div>`,{wide:true});
  const form=$('form',dialog),results=$('.search-results',dialog);let token=0;
  form.addEventListener('submit',async event=>{
    event.preventDefault();const query=form.elements.q.value;if(!query.trim())return;const current=++token;results.innerHTML=loading('Searching MDBList…');results.setAttribute('aria-busy','true');
    await busy($('[type=submit]',form),async()=>{try{
      const response=await api('/api/search?'+new URLSearchParams({q:query,type:form.elements.type.value}));if(!dialog.open||current!==token)return;
      const rows=asArray(response.items).map(normalizeSearchRow);results.innerHTML=rows.length?rows.map((r,index)=>{const valid=['movie','show'].includes(r.type)&&(r.provider==='imdb'?/^tt\d+$/:/^\d+$/).test(r.id||'');return `<article class="search-result">${art(r.poster,'')}<div><h3>${e(r.title)}</h3><small>${e(mediaName(r.type))} · ${e(r.year||'Year unknown')} · ${r.id?e(r.provider.toUpperCase()+' '+r.id):'Source ID unavailable'}</small>${valid?'':'<p class="field-help">A compatible type and valid source identity are required.</p>'}</div><button class="btn small" data-search-index="${index}" ${valid?'':'disabled'}>Add to collection</button></article>`;}).join(''):empty('No titles found.','Try another spelling or media type. MDBList may not cover every title.',{name:'search'});
      $$('[data-search-index]',results).forEach(button=>button.addEventListener('click',async()=>{
        const row=rows[Number(button.dataset.searchIndex)];const body={provider:row.provider,id:row.id,type:row.type};
        await busy(button,async()=>{lockDialog(dialog,true);try{
          const added=await api('/api/releases',{method:'POST',body,timeout:120000});if(!added.id)throw new Error('The server returned no local release identity. Refresh your collection before retrying.');const verified=await api(`/api/releases/${encodeURIComponent(added.id)}`);
          if(verified.type!==row.type||String(verified[row.provider+'_id'])!==row.id)throw new Error('The saved release did not match the selected source identity. Check your collection before retrying.');remember([verified]);
          // Remove the add handler on success so a verified local write cannot
          // accidentally be repeated when the result becomes a view link.
          const view=document.createElement('button');view.className='btn small acquired';view.textContent='View release';view.addEventListener('click',()=>{dialog.close();openRelease(verified.id);});button.replaceWith(view);view.focus();toast(`${verified.title} added to your collection.`);if($('#release-grid'))await loadReleases();
        }catch(error){dialogError(dialog,error);}finally{lockDialog(dialog,false);}},'Adding…');
      }));
    }catch(error){if(dialog.open&&current===token)results.innerHTML=empty('Search unavailable',error.message,{name:'info',error:true});}finally{if(current===token)results.removeAttribute('aria-busy');}},'Searching…');
  });
  return dialog;
}
function secretField(name, label, configured) {
  return `<label>${e(label)}<input type="password" name="${e(name)}" autocomplete="new-password" placeholder="${configured?'Saved — leave blank to keep unchanged':'Not configured'}"><small class="saved-secret">${configured?'A credential is stored securely. It is never sent back to this browser.':'Enter a credential, then save before testing.'}</small></label>`;
}
function toggleField(name, title, subtitle, checked) {
  return `<label class="switch"><b>${e(title)}<small>${e(subtitle)}</small></b><input type="checkbox" name="${e(name)}" ${checked?'checked':''}></label>`;
}
function settingsHeader(title, text, mark, configured) {
  return `<header><span class="integration-mark">${e(mark)}</span><div><h2>${e(title)}</h2><p>${e(text)}</p></div>${configured===undefined?'':`<span class="configured ${configured?'':'unconfigured'}">${configured?'Configured':'Not configured'}</span>`}</header>`;
}
function optionMarkup(items, value, valueKey, labelKey, placeholder) {
  const selected=String(value??'');const rows=asArray(items);
  return `<option value="">${e(placeholder)}</option>${selected&&!rows.some(row=>String(row[valueKey])===selected)?`<option selected value="${e(selected)}">${e(selected)} (saved)</option>`:''}${rows.map(row=>`<option value="${e(row[valueKey])}" ${String(row[valueKey])===selected?'selected':''}>${e(row[labelKey])}</option>`).join('')}`;
}
function arrSettingsSection(kind, s={}) {
  return `<section class="settings-section" id="settings-${kind}">${settingsHeader(targetName(kind),kind==='radarr'?'Movies, sent to your own library.':'Series, sent to your own library.',kind==='radarr'?'R':'S',s.configured)}<div class="form-grid"><label>Server URL<input type="url" name="${kind}.url" value="${e(s.url)}" placeholder="http://your-server:port" autocomplete="off"></label>${secretField(`${kind}.api_key`,'API key',s.configured)}<label>Quality profile<select name="${kind}.quality_profile_id">${optionMarkup([],s.quality_profile_id||'','id','name','Load profiles from saved server')}</select></label><label>Root folder<select name="${kind}.root_folder">${optionMarkup([],s.root_folder,'path','path','Load folders from saved server')}</select></label></div>${toggleField(`${kind}.enabled`,'Enable acquisition','Allow explicit and automated adds to this integration.',s.enabled)}${toggleField(`${kind}.search`,'Search on automatic adds','Automatic actions may start downloads. Manual adds require their own confirmation.',s.search)}<div class="settings-actions"><button type="button" class="btn small" data-action="integration-test" data-kind="${kind}">${icon('refresh')}Test connection</button><button type="button" class="btn small" data-action="integration-options" data-kind="${kind}">Load profiles & folders</button><output id="${kind}-result" aria-live="polite">Save connection changes before testing or loading options.</output></div></section>`;
}
function tmdbSettingsSection(s={}) {
  return `<section class="settings-section" id="settings-tmdb">${settingsHeader('TMDb fallback','Optional enrichment. MDBList remains the primary metadata source.','T',s.configured)}<div class="form-grid">${secretField('tmdb.api_key','TMDb API key',s.configured)}<div class="field-help"><a class="text-button" href="https://www.themoviedb.org/settings/api" target="_blank" rel="noopener noreferrer">Get a TMDb API key</a><p>Leave the API key blank to preserve the saved credential. Save before testing; the test uses only the saved key.</p></div></div>${toggleField('tmdb.enabled','Enable TMDb fallback','Inactive when disabled or without a saved API key, even if checked.',s.enabled)}<p class="notice info">TMDb fills missing person information and supplies verified cast / credits. Historical discovery is bounded and cached, not a complete filmography. No automatic downloads.</p><div class="settings-actions"><button type="button" class="btn small" data-action="integration-test" data-kind="tmdb">${icon('refresh')}Test connection</button><output id="tmdb-result" aria-live="polite">${s.enabled&&s.configured?'Enabled with a saved key':'Inactive'} · Save before testing the saved key.</output></div></section>`;
}
function siloSettingsSection(s={}) {
  return `<section class="settings-section" id="settings-silo">${settingsHeader('Silo','Read-only exact-ID local file verification. Never starts a download.','S',s.configured)}<div class="form-grid"><label>Silo server URL<input type="url" name="silo.url" value="${e(s.url||'')}" autocomplete="off"></label>${secretField('silo.api_key','Silo API key',s.configured)}</div>${toggleField('silo.enabled','Enable Silo availability checks','A verified local file is independent of Sonarr / Radarr library presence.',s.enabled)}<div class="settings-actions"><button type="button" class="btn small" data-action="integration-test" data-kind="silo">Test Silo connection</button><output id="silo-result" aria-live="polite">${s.enabled?'Enabled':'Disabled'} · Save changes before testing.</output></div><div class="settings-actions"><button type="button" class="btn small" data-action="inventory-sync" data-kind="silo">Sync Silo availability</button><output id="silo-sync-result" aria-live="polite">Unknown and failed checks never mean absent.</output></div><div class="settings-actions"><button type="button" class="btn small" data-action="inventory-sync" data-kind="arr">Sync Arr inventory</button><output id="arr-sync-result" aria-live="polite">Read-only Sonarr / Radarr inventory. Does not mean a local file is available.</output></div><a class="text-button" href="#/activity">View sync activity</a></section>`;
}
async function syncInventory(kind,button) {
  if(!['silo','arr'].includes(kind))throw new Error('Unknown inventory.');if(state.dirty)throw new Error('Save or reset settings before syncing saved connections.');
  const output=$(`#${kind}-sync-result`);
  await busy(button,async()=>{try{const result=await api(`/api/${kind}/sync`,{method:'POST',body:{}});if(result.ok!==true)throw new Error('The server did not confirm the sync request.');if(output?.isConnected)output.textContent=kind==='silo'?'Silo availability sync queued. Check Activity for completion.':'Arr inventory sync queued. No download searches requested.';toast('Read-only sync queued. Check Activity for completion.');}catch(error){if(output?.isConnected)output.textContent=error.message;throw error;}},'Queuing…');
}
function listPoliciesMarkup(m) {
  const lists=Array.isArray(m.lists)?m.lists:String(m.list_ids||'').split(',').filter(Boolean).map(id=>({id,name:'',enabled:true,monitored:false,auto_add:false}));
  return `<input type="hidden" name="mdblist.list_count" value="${lists.length}">${lists.map(listPolicyRow).join('')}`;
}
function listPolicyRow(row={},index=0) {
  const prefix=`mdblist.lists.${index}.`;
  return `<fieldset class="list-policy-row"><legend>Personal list ${index+1}</legend><div class="form-grid"><label>List ID<input required name="${prefix}id" value="${e(row.id||'')}" autocomplete="off" pattern="[0-9]+"></label><label>List name<input name="${prefix}name" value="${e(row.name||'')}" maxlength="300"></label></div><div class="actions">${[['enabled','Enabled'],['monitored','Monitor'],['auto_add','Auto-add']].map(([key,label])=>`<label class="check"><input type="checkbox" name="${prefix+key}" ${row[key]?'checked':''}>${label}</label>`).join('')}<button type="button" class="btn small danger" data-action="list-remove">Remove list</button></div></fieldset>`;
}
function editListRows(button,add=false) {
  const container=$('#personal-lists');
  if(add)container.insertAdjacentHTML('beforeend',listPolicyRow({},$$('.list-policy-row',container).length));else button.closest('.list-policy-row').remove();
  const rows=$$('.list-policy-row',container);rows.forEach((row,index)=>{ $('legend',row).textContent=`Personal list ${index+1}`;$$('[name]',row).forEach(input=>{input.name=input.name.replace(/lists\.\d+\./,`lists.${index}.`);});});
  $('[name="mdblist.list_count"]',container).value=rows.length;markSettingsDirty();if(add)$('input',rows.at(-1)).focus();
}
function renderSettings(settings) {
  const m=settings.mdblist||{},p=settings.pushover||{},a=settings.automation||{};
  $('#main').innerHTML=heading('Make it yours.','Your sources, your library, your rules.')+`<div class="settings-layout"><nav class="settings-index" aria-label="Settings sections">${[['mdblist','MDBList'],['tmdb','TMDb fallback'],['radarr','Radarr'],['sonarr','Sonarr'],['silo','Silo'],['pushover','Pushover'],['automation','Automation'],['general','General']].map(([id,label])=>`<a href="#settings-${id}" data-setting-anchor="${id}">${icon('settings')}${label}</a>`).join('')}</nav><form id="settings-form"><fieldset class="settings-fields"><section class="settings-section" id="settings-mdblist">${settingsHeader('MDBList','Primary metadata, list discovery, and person biographies.','M',m.configured)}<div class="form-grid">${secretField('mdblist.api_key','MDBList API key',m.configured)}<label>Sync interval (minutes)<input type="number" min="5" max="10080" required name="mdblist.sync_interval_minutes" value="${e(m.sync_interval_minutes||360)}"></label></div><div id="personal-lists">${listPoliciesMarkup(m)}</div><button type="button" class="btn small" data-action="list-add">${icon('plus')}Add personal list</button><p class="field-help">Discovery never enables monitoring or auto-add. New rows start with every opt-in off; preserve literal source IDs.</p><div></div><div class="notice info">MDBList is the primary metadata source. Optional TMDb fallback fills missing person information and supplies verified cast / credits. Coverage remains incomplete; no cast matches are guessed.</div><div class="settings-actions"><button type="button" class="btn small" data-action="sync">${icon('refresh')}Sync saved sources now</button><span class="field-help">There is no separate MDBList test endpoint. Save, sync, then check Activity.</span></div></section>${tmdbSettingsSection(settings.tmdb)}${arrSettingsSection('radarr',settings.radarr)}${arrSettingsSection('sonarr',settings.sonarr)}${siloSettingsSection(settings.silo)}<section class="settings-section" id="settings-pushover">${settingsHeader('Pushover','Private notifications. No test is sent automatically.','P',p.configured)}<div class="form-grid">${secretField('pushover.token','Application token',p.configured)}${secretField('pushover.user','User / group key',p.configured)}<label class="span2">Device (optional)<input name="pushover.device" value="${e(p.device)}" autocomplete="off"></label></div>${toggleField('pushover.enabled','Enable notifications','Per-actress Notify and global automation must also be enabled.',p.enabled)}<div class="settings-actions"><button type="button" class="btn small" data-action="integration-test" data-kind="pushover">${icon('bell')}Send one test notification</button><output id="pushover-result" aria-live="polite">This button sends a real notification after confirmation.</output></div></section><section class="settings-section" id="settings-automation">${settingsHeader('Automation','Disabled by default. You decide when actions become automatic.','A')}${toggleField('automation.enabled','Enable global automation','Only monitored watches with Auto-add or Notify enabled can trigger actions.',a.enabled)}<p class="notice info">The first synchronization establishes a silent baseline. It never sends historical notifications or starts automatic acquisitions.</p><p class="field-help">Acquisition is routed by actual media type. Each integration must be enabled and have a valid quality profile and root folder.</p></section><section class="settings-section" id="settings-general">${settingsHeader('General','Your self-hosted instance.','G')}<label>Public URL (optional)<input type="url" name="public_url" value="${e(settings.public_url)}" placeholder="https://your-instance.example" autocomplete="off"><small>Used in notification links and secure-cookie configuration. HTTP(S), without user information, query, or fragment.</small></label><p class="field-help">Passwords and API keys remain on your server. Blank secret fields preserve saved credentials; they do not delete them.</p></section></fieldset><p class="form-error" id="settings-error" role="alert" hidden></p><div class="settings-save"><p id="settings-save-status" role="status">All changes saved.</p><div class="actions"><button type="button" class="btn" data-action="settings-reset">Reset changes</button><button type="submit" class="btn primary">Save settings</button></div></div></form></div>`;
  const form=$('#settings-form');form.addEventListener('input',markSettingsDirty);form.addEventListener('change',markSettingsDirty);form.addEventListener('submit',saveSettings);
  form.insertAdjacentHTML('afterend',`<section class="settings-section task-settings" id="settings-schedules">${settingsHeader('Scheduled tasks','Independent, persisted schedules. New tasks start disabled.','T')}<div class="actions"><button type="button" class="btn small" data-action="task-add">${icon('plus')}Add scheduled task</button><button type="button" class="btn small" data-action="task-refresh">Refresh scheduled tasks</button></div><p class="field-help">Dates and times use each task’s IANA timezone. Search missing is limited to app-monitored releases and requires task consent plus saved global and Arr opt-ins. No whole-library searches.</p><div id="task-list" aria-live="polite">${loading('Loading scheduled tasks…')}</div></section>`);
  $('.settings-index').insertAdjacentHTML('beforeend','<a href="#settings-schedules" data-setting-anchor="schedules">'+icon('calendar')+'Schedules</a>');
  loadTasks();
}
function taskRows(items) {
  return items.length?items.map(task=>`<article class="task-row" data-task-id="${e(task.id)}"><div class="row wrap"><h3>${e(task.name)}</h3><span class="badge ${task.enabled?'teal':''}">${task.enabled?'Enabled':'Disabled'}</span><span class="badge">${e(task.last_status||'Never run')}</span></div><p>${e(taskTypes[task.type]||task.type)} · ${e(task.frequency)}${task.frequency==='interval'?` / ${e(task.interval_minutes)} minutes`:''}${task.frequency==='weekly'?` / ${asArray(task.weekdays).map(day=>e(weekdays[day]||'Unknown day')).join(', ')}`:''}</p><p class="field-help">Start ${e(task.date)} ${e(task.time)} · ${e(task.timezone)} · Download-search consent ${task.allow_download_search?'on':'off'}</p><dl class="detail-facts">${detailFact('Next run',task.next_run?formatDate(task.next_run,{dateStyle:'medium',timeStyle:'short'}):task.enabled?'No future run':'Schedule disabled')}${detailFact('Last run',task.last_run?formatDate(task.last_run,{dateStyle:'medium',timeStyle:'short'}):'Never run')}</dl>${task.last_error?`<p class="form-error" role="alert">${e(task.last_error)}</p>`:''}<div class="actions">${[['edit','Edit task'],['toggle',task.enabled?'Disable task':'Enable task'],['run','Run now'],['delete','Delete task']].map(([action,label])=>`<button type="button" class="btn small ${action==='delete'?'danger':''}" data-action="task-${action}" data-id="${e(task.id)}" ${task.last_status==='running'?'disabled':''}>${label}</button>`).join('')}</div></article>`).join(''):'<p class="field-help">No scheduled tasks yet.</p>';
}
async function readTasks() {
  const data=await api('/api/tasks');if(!Array.isArray(data.items))throw new Error('Invalid scheduled-task response.');
  const ids=new Set();for(const task of data.items){if(!Number.isSafeInteger(task.id)||task.id<1||ids.has(task.id))throw new Error('Invalid or duplicate scheduled-task identity.');ids.add(task.id);}
  return data.items;
}
function drawTasks(items,node=$('#task-list')) {if(!node?.isConnected)return;taskItems=new Map(items.map(task=>[String(task.id),task]));node.innerHTML=taskRows(items);}
async function loadTasks() {
  const node=$('#task-list');if(!node)return;const token=node.taskToken=(node.taskToken||0)+1;node.innerHTML=loading('Loading scheduled tasks…');
  try{const items=await readTasks();if(token===node.taskToken)drawTasks(items,node);}
  catch(error){if(node.isConnected&&token===node.taskToken)node.innerHTML=`<p class="form-error" role="alert">${e(error.message)}</p><button type="button" class="btn small" data-action="task-refresh">Retry scheduled tasks</button>`;}
}
async function verifyTask(id,expected) {
  const items=await readTasks(),task=items.find(item=>String(item.id)===String(id));
  if(!task||!settingsMatch(task,expected))throw new Error('Task readback did not match the requested settings. Refresh scheduled tasks before retrying.');
  drawTasks(items);return task;
}
function editTask(task=null) {
  const value=task||{name:'',type:'catalog_sync',enabled:false,frequency:'daily',date:localDate(),time:'09:00',timezone:Intl.DateTimeFormat().resolvedOptions().timeZone||'UTC',interval_minutes:60,weekdays:[],allow_download_search:false};
  const dialog=makeDialog(task?'Edit scheduled task':'Add scheduled task',`<form id="task-form"><div class="form-grid"><label class="span2">Task name<input name="name" required maxlength="200" value="${e(value.name)}" autofocus></label><label class="span2">Task type<select name="type" aria-label="Task type">${Object.entries(taskTypes).map(([key,label])=>`<option value="${key}" ${value.type===key?'selected':''}>${e(label)}</option>`).join('')}</select></label><label>Recurrence<select name="frequency" aria-label="Recurrence">${['once','interval','daily','weekly'].map(key=>`<option value="${key}" ${value.frequency===key?'selected':''}>${key}</option>`).join('')}</select></label><label data-task-interval>Interval (minutes)<input name="interval_minutes" type="number" min="1" max="525600" value="${e(value.interval_minutes||60)}"></label><label>Start date<input name="date" type="date" required value="${e(value.date)}"></label><label>Time<input name="time" type="time" required value="${e(value.time)}"></label><label class="span2">Timezone (IANA)<input name="timezone" required value="${e(value.timezone)}" placeholder="Europe/Amsterdam"></label></div><fieldset data-task-weekdays><legend>Days of week</legend><div class="actions">${weekdays.map((day,index)=>`<label class="check"><input type="checkbox" name="weekdays" value="${index}" ${asArray(value.weekdays).includes(index)?'checked':''}>${day}</label>`).join('')}</div></fieldset><label class="check"><input type="checkbox" name="enabled" ${value.enabled?'checked':''}>Enable schedule</label><div data-task-search><label class="check"><input type="checkbox" name="allow_download_search" ${value.allow_download_search?'checked':''}>Allow download searches for app-monitored missing releases</label><p class="notice error">Explicit opt-in: searches may download media. Only app-monitored releases are eligible, never an entire Arr library. Saved global automation, enabled Arr integration, and search settings must also allow this.</p></div><p class="field-help">A past once-only date does not automatically replay. The server validates IANA timezone and daylight-saving gaps. Saving a schedule does not run it immediately.</p><p class="form-error" hidden role="alert"></p><div class="dialog-actions"><button type="button" class="btn" data-cancel>Cancel</button><button type="submit" class="btn primary">Save task</button></div></form>`,{narrow:true});
  const form=$('#task-form',dialog);
  const conditions=()=>{const interval=form.elements.frequency.value==='interval',weekly=form.elements.frequency.value==='weekly',search=form.elements.type.value==='search_missing';$('[data-task-interval]',form).hidden=!interval;form.elements.interval_minutes.required=interval;$('[data-task-weekdays]',form).hidden=!weekly;$('[data-task-search]',form).hidden=!search;if(!search)form.elements.allow_download_search.checked=false;};
  form.elements.frequency.addEventListener('change',conditions);form.elements.type.addEventListener('change',conditions);conditions();$('[data-cancel]',dialog).addEventListener('click',()=>dialog.close());
  form.addEventListener('submit',async event=>{
    event.preventDefault();if(dialog.dataset.pending==='true')return;$('.form-error',dialog).hidden=true;
    const data=new FormData(form),body={name:String(data.get('name')),type:String(data.get('type')),enabled:data.has('enabled'),frequency:String(data.get('frequency')),date:String(data.get('date')),time:String(data.get('time')),timezone:String(data.get('timezone')),interval_minutes:Number(data.get('interval_minutes'))||60,weekdays:data.getAll('weekdays').map(Number),allow_download_search:data.get('type')==='search_missing'&&data.has('allow_download_search')};
    if(body.frequency==='weekly'&&!body.weekdays.length){dialogError(dialog,new Error('Select at least one weekday.'));return;}
    if(body.type==='search_missing'&&body.enabled&&!body.allow_download_search){dialogError(dialog,new Error('Enabled search tasks require explicit download-search consent.'));return;}
    await busy($('[type=submit]',form),async()=>{lockDialog(dialog,true);try{const saved=await api(task?`/api/tasks/${encodeURIComponent(task.id)}`:'/api/tasks',{method:task?'PATCH':'POST',body});await verifyTask(saved.id||task?.id,body);dialog.close();toast('Scheduled task saved and verified.');}catch(error){dialogError(dialog,error);}finally{lockDialog(dialog,false);}},'Saving task…');
  });return dialog;
}
async function taskAction(action,id,button) {
  const task=taskItems.get(String(id));if(!task)throw new Error('Refresh scheduled tasks before taking this action.');
  if(action==='edit')return editTask(task);
  if(state.dirty&&action==='run')throw new Error('Save or reset settings before running tasks against saved connections.');
  await busy(button,async()=>{
    if(action==='delete'){
      if(!await confirmAction('Delete this scheduled task?',`Delete ${task.name}? This removes its schedule, not releases or external media.`,'Delete scheduled task'))return;
      await api(`/api/tasks/${encodeURIComponent(id)}`,{method:'DELETE'});const items=await readTasks();if(items.some(item=>String(item.id)===String(id)))throw new Error('The task is still present after deletion. Refresh before retrying.');drawTasks(items);toast('Scheduled task deletion verified.');
    }else if(action==='toggle'){
      if(!task.enabled&&task.type==='search_missing'&&!task.allow_download_search)throw new Error('Edit this task and explicitly allow download searches before enabling it.');
      if(!task.enabled&&task.type==='search_missing'&&!await confirmAction('Enable download-search schedule?',`Enable ${task.name}? Future runs may download app-monitored missing releases.`,'Enable search schedule'))return;
      const body={enabled:!task.enabled};await api(`/api/tasks/${encodeURIComponent(id)}`,{method:'PATCH',body});await verifyTask(id,body);toast('Schedule state verified.');
    }else if(action==='run'){
      if(task.type==='search_missing'&&!task.allow_download_search)throw new Error('Edit this task and explicitly allow download searches before running it.');
      if(task.type==='search_missing'&&!await confirmAction('Run download searches now?',`Run ${task.name}? This may download app-monitored missing releases.`,'Run monitored searches'))return;
      const result=await api(`/api/tasks/${encodeURIComponent(id)}/run`,{method:'POST',body:{}});if(result.ok!==true)throw new Error('The server did not accept the task run.');const current=await verifyTask(id,{});if(!current.last_run||current.last_run===task.last_run)throw new Error('The run was accepted but a new execution is not yet visible. Refresh tasks or Activity before retrying.');toast('Task execution recorded. Refresh tasks or Activity for its completion.');
    }
  });
}
function markSettingsDirty() {state.dirty=true;const node=$('#settings-save-status');if(node){node.textContent='Unsaved changes';node.className='dirty';}}
export function settingsMatch(saved, payload) {
  const equal=(actual,expected)=>{
    if(Array.isArray(expected))return Array.isArray(actual)&&actual.length===expected.length&&expected.every((value,index)=>equal(actual[index],value));
    if(expected&&typeof expected==='object')return Object.entries(expected).every(([key,value])=>['api_key','token','user'].includes(key)||equal(actual?.[key],value));
    return actual===expected;
  };
  return equal(saved,payload);
}
async function saveSettings(event) {
  event.preventDefault();const form=event.currentTarget;if(!form.reportValidity())return;const body=buildSettingsPayload(new FormData(form));const error=$('#settings-error');error.hidden=true;
  await busy($('[type=submit]',form),async()=>{
    $('.settings-fields',form).disabled=true;
    try {
      await api('/api/settings',{method:'PUT',body});const saved=await api('/api/settings');
      if(!settingsMatch(saved,body))throw new Error('Settings readback did not match your changes. Reload and review before retrying.');
      state.settings=saved;if(Array.isArray(saved.mdblist?.lists))state.listPolicies=saved.mdblist.lists;state.dirty=false;renderSettings(saved);toast('Settings saved and verified.');
    }catch(err){if(error.isConnected){error.textContent=err.message;error.hidden=false;}}finally{const fields=$('.settings-fields',form);if(fields)fields.disabled=false;}
  },'Saving…');
}
async function integrationAction(kind, options, button) {
  if(!['radarr','sonarr','pushover','silo','tmdb'].includes(kind))throw new Error('Unknown integration.');
  if(state.dirty)throw new Error('Save or reset your settings changes first. Connection actions use saved credentials.');
  if(!options&&kind==='pushover'&&!await confirmAction('Send a test notification?','This sends one real Pushover notification using your saved credentials.','Send one notification'))return;
  const output=$(`#${kind}-result`);if(output){output.className='';output.textContent=options?'Loading profiles and folders…':'Testing connection…';}
  await busy(button,async()=>{try {
    const data=await api(`/api/integrations/${kind}/${options?'options':'test'}`,{method:options?'GET':'POST',...(options?{}:{body:{}})});
    if(options){if(!button.isConnected)return;const form=$('#settings-form');const profiles=form.elements.namedItem(`${kind}.quality_profile_id`),folders=form.elements.namedItem(`${kind}.root_folder`);profiles.innerHTML=optionMarkup(data.quality_profiles,profiles.value,'id','name','Select a quality profile');folders.innerHTML=optionMarkup(data.root_folders,folders.value,'path','path','Select a root folder');if(output)output.textContent=`${asArray(data.quality_profiles).length} profiles and ${asArray(data.root_folders).length} folders loaded. Select values, then save.`;}
    else {if(data.ok!==true)throw new Error('The server did not confirm a successful test.');if(output)output.textContent=kind==='pushover'?'One test notification sent.':'Connection verified.';toast(kind==='pushover'?'Test notification sent.':`${kind==='tmdb'?'TMDb':kind==='silo'?'Silo':targetName(kind)} connection verified.`);}
  }catch(error){if(output?.isConnected){output.className='form-error';output.textContent=error.message;}throw error;}},options?'Loading…':'Testing…');
}
function confirmAction(title, message, label='Continue') {
  return new Promise(resolve=>{const dialog=makeDialog(title,`<p class="no-results-note">${e(message)}</p><div class="dialog-actions"><button class="btn" data-cancel autofocus>Cancel</button><button class="btn primary" data-confirm>${e(label)}</button></div>`,{narrow:true});let confirmed=false;dialog.addEventListener('close',()=>resolve(confirmed),{once:true});$('[data-cancel]',dialog).addEventListener('click',()=>dialog.close());$('[data-confirm]',dialog).addEventListener('click',()=>{confirmed=true;dialog.close();});});
}
async function logout(button) {
  if(state.dirty&&!await confirmAction('Sign out without saving?','Your unsaved settings will be discarded.','Sign out'))return;
  await busy(button,async()=>{await api('/api/logout',{method:'POST',body:{}});const session=await api('/api/session');if(session.authenticated)throw new Error('The session is still active. Sign out was not verified.');state.dirty=false;state.settings=null;state.dashboard=null;state.releases.clear();state.actresses=[];showAuth(session.needs_setup);},'Signing out…');
}
async function handleAction(button) {
  const action=button.dataset.action,id=button.dataset.id;
  if(action==='task-add')return editTask();
  if(action==='task-refresh')return loadTasks();
  if(['task-edit','task-toggle','task-run','task-delete'].includes(action))return taskAction(action.slice(5),id,button);
  if(action==='list-add'||action==='list-remove')return editListRows(button,action==='list-add');
  if(action==='retry')return navigate();
  if(action==='more-releases')return loadReleases(true);
  if(action==='retry-releases')return loadReleases();
  if(action==='search')return openSearch();
  if(action==='release')return openRelease(id);
  if(action==='actress')return openActress(id);
  if(action==='person')return openPerson(id);
  if(action==='cast-retry')return loadReleaseCast(button.closest('dialog'),button.dataset.releaseId);
  if(action==='acquire')return acquireRelease(id);
  if(action==='monitor')return monitorRelease(id,button);
  if(action==='watchlist-retry')return loadWatchlist(button.closest('dialog'),id);
  if(action==='watchlist-set')return setWatchlist(id,button);
  if(action==='inventory-sync')return syncInventory(button.dataset.kind,button);
  if(action==='sync'){if(state.dirty)throw new Error('Save or reset settings before syncing saved sources.');return runSync(button);}
  if(action==='logout')return logout(button);
  if(action==='add-actress')return editActress();
  if(action==='import-actresses')return importActresses();
  if(action==='page'){state.filters.page=Number(button.dataset.page)||1;return loadReleases();}
  if(action==='clear-filters'){state.filters={q:'',window:'all',type:'all',group:'',sort:'release',page:1};return navigate();}
  if(action==='calendar-today'){state.calendar=new Date(new Date().getFullYear(),new Date().getMonth(),1);return drawCalendar();}
  if(action==='calendar-prev'||action==='calendar-next'){state.calendar.setMonth(state.calendar.getMonth()+(action==='calendar-next'?1:-1));return drawCalendar();}
  if(action==='integration-test'||action==='integration-options')return integrationAction(button.dataset.kind,action==='integration-options',button);
  if(action==='settings-reset'){if(state.dirty&&!await confirmAction('Reset unsaved changes?','Saved settings will be restored. New credentials typed into this form will be discarded.','Reset changes'))return;state.dirty=false;return renderSettings(state.settings);}
}
async function routeChanged() {
  if(state.route==='settings'&&state.dirty){if(!await confirmAction('Leave unsaved settings?','Your changes have not been saved. Leave this page and discard them?','Discard changes')){history.replaceState(null,'','#/settings');return;}state.dirty=false;}
  closeDialogs();await navigate();
}
export async function initialize() {
  if(!document.body||document.body.dataset.mrwReady)return;document.body.dataset.mrwReady='true';
  $('#auth-form').addEventListener('submit',authenticate);
  document.addEventListener('click',event=>{
    const anchor=event.target.closest('[data-setting-anchor]');if(anchor){event.preventDefault();$(`#settings-${anchor.dataset.settingAnchor}`)?.scrollIntoView({behavior:'smooth',block:'start'});return;}
    if(event.target.closest('.skip-link')){event.preventDefault();$('#main').focus();return;}
    const button=event.target.closest('[data-action]');if(!button||button.disabled)return;event.preventDefault();Promise.resolve(handleAction(button)).catch(error=>toast(error.message,true));
  });
  document.addEventListener('error',event=>{if(event.target.matches?.('.art img,.hero-art')){if(event.target.matches('.art img'))event.target.parentElement.classList.add('missing');event.target.remove();}},true);
  window.addEventListener('hashchange',()=>routeChanged().catch(error=>toast(error.message,true)));
  window.addEventListener('beforeunload',event=>{if(state.dirty){event.preventDefault();event.returnValue='';}});
  document.addEventListener('visibilitychange',()=>{if(!document.hidden)poll();});
  setInterval(poll,15000);
  await initializeSession();
}
async function initializeSession() {
  const boot=$('#boot');boot.hidden=false;
  try {const session=await api('/api/session');state.session=session;if(session.authenticated)await startApp();else showAuth(session.needs_setup);}
  catch(error){boot.innerHTML=empty('Couldn’t connect to your instance',error.message,{name:'info',error:true,actions:'<button class="btn" id="retry-session">Retry connection</button>'});$('#retry-session').addEventListener('click',initializeSession,{once:true});}
}
if(typeof document!=='undefined') {
  if(document.readyState==='loading')document.addEventListener('DOMContentLoaded',()=>initialize().catch(error=>toast(error.message,true)),{once:true});
  else initialize().catch(error=>toast(error.message,true));
}
function deleteActress(a,parent) {const dialog=makeDialog('Remove this actress watch?',`<p class="no-results-note">Remove <strong>${e(a.name)}</strong> from your local watches? This does not delete releases or change your MDBList, Radarr, or Sonarr accounts.</p><p class="form-error" hidden role="alert"></p><div class="dialog-actions"><button class="btn" data-cancel>Keep watch</button><button class="btn danger" data-confirm>Remove local watch</button></div>`,{narrow:true});$('[data-cancel]',dialog).addEventListener('click',()=>dialog.close());$('[data-confirm]',dialog).addEventListener('click',event=>busy(event.currentTarget,async()=>{try{await api(`/api/actresses/${a.id}`,{method:'DELETE'});await refreshActressCache();if(state.actresses.some(item=>item.id===a.id))throw new Error('The watch is still present after deletion.');dialog.close();parent.close();toast('Local actress watch removed.');if(state.route==='actresses'){renderActressesPage();await loadActresses();}}catch(error){dialogError(dialog,error);}},'Removing…'));}
