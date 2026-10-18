import test, { before, after } from 'node:test';
import assert from 'node:assert/strict';
import { readFile, readdir, access } from 'node:fs/promises';
import { createServer } from 'node:http';
import { chromium } from '@playwright/test';

let browser, server, origin;
const assets = new Map(await Promise.all(['index.html','app.js','styles.css'].map(async name => [name, await readFile(new URL('../'+name, import.meta.url),'utf8')])));
const defaults = () => ({mdblist:{configured:false,list_ids:'',sync_interval_minutes:360},sonarr:{url:'',configured:false,quality_profile_id:0,root_folder:'',enabled:false,search:false},radarr:{url:'',configured:false,quality_profile_id:0,root_folder:'',enabled:false,search:false},pushover:{configured:false,enabled:false,device:''},automation:{enabled:false,notify_first_sync:false},public_url:''});
const release = () => ({id:1,title:'Handler test title',type:'movie',year:2026,tmdb_id:'654321',imdb_id:'tt7654321',description:'Test-only metadata.',genres:['Drama'],release_date:'2026-01-01',monitored:false,actresses:[],acquisition:{},content_info:{advisory:'Source supplied'},ratings:[],watch_providers:[]});
const actress = () => ({id:2,name:'Handler test watch',tmdb_id:'123456',group:'',monitored:true,auto_add:false,notify:false,release_count:1,upcoming_count:0,metadata_status:'unknown'});

before(async () => {
  let executablePath=process.env.CHROMIUM_EXECUTABLE;
  if(!executablePath) {
    try { await access(chromium.executablePath()); executablePath=chromium.executablePath(); }
    catch { try { const dirs=await readdir('/opt/hermes/tools'); const dir=dirs.filter(x=>/^chromium-\d+$/.test(x)).sort().reverse()[0]; if(dir)executablePath=`/opt/hermes/tools/${dir}/chrome-linux64/chrome`; } catch {} }
  }
  browser=await chromium.launch({headless:true,executablePath,args:['--no-sandbox']});
  server=createServer((req,res)=>{const name=req.url==='/'?'index.html':req.url.slice(1);if(!assets.has(name)){res.writeHead(404).end();return;}res.setHeader('Content-Type',name.endsWith('.js')?'text/javascript':name.endsWith('.css')?'text/css':'text/html');res.end(assets.get(name));});
  await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));origin=`http://127.0.0.1:${server.address().port}`;
});
after(async()=>{await browser?.close();if(server)await new Promise(resolve=>server.close(resolve));});
async function app({authenticated=true,needsSetup=false,withRelease=false,configuredArr=false,hash=''}={}) {
  const page=await browser.newPage();page.setDefaultTimeout(4000);
  const requests=[],errors=[];page.on('pageerror',error=>errors.push(error.message));
  let settings=defaults(),item=release(),person=actress();
  if(configuredArr)settings.radarr={url:'http://arr.test',configured:true,enabled:true,search:true,root_folder:'/media/movies',quality_profile_id:7};
  await page.route('**/api/**',async route=>{
    const request=route.request(),url=new URL(request.url()),path=url.pathname,method=request.method();const body=request.postDataJSON();requests.push({path,method,body,query:Object.fromEntries(url.searchParams)});
    let data={ok:true},status=200;
    if(path==='/api/session')data={authenticated,needs_setup:needsSetup};
    else if(path==='/api/login'||path==='/api/setup'){authenticated=true;needsSetup=false;data={authenticated:true};}
    else if(path==='/api/logout')authenticated=false;
    else if(path==='/api/dashboard')data={stats:{releases:withRelease?1:0,actresses:1,upcoming:0,current:0,past:withRelease?1:0,monitored:0},featured:[],upcoming:[],recent:[],activity:[],sync:{running:false}};
    else if(path==='/api/settings'){if(method==='PUT'){const old=settings;settings=body;for(const kind of ['mdblist','sonarr','radarr','pushover']){settings[kind].configured=old[kind].configured||Boolean(body[kind].api_key||body[kind].token);delete settings[kind].api_key;delete settings[kind].token;delete settings[kind].user;}}data=settings;}
    else if(path==='/api/releases')data=method==='POST'?(withRelease=true,item):{items:withRelease?[item]:[],total:withRelease?1:0,page:Number(url.searchParams.get('page')||1),limit:Number(url.searchParams.get('limit')||36)};
    else if(path==='/api/releases/1'){if(method==='PATCH')item={...item,...body};data=item;}
    else if(path==='/api/releases/1/acquire'){item.acquisition.radarr='added';data={ok:true,result:{id:11,already_exists:false,url:'http://arr.test/movie/11'}};}
    else if(path==='/api/actresses')data={items:[person],total:1};
    else if(path==='/api/actresses/2')data={actress:person,releases:withRelease?[item]:[],total:withRelease?1:0};
    else if(path==='/api/actresses/2/acquire'){item.acquisition.radarr='added';data={results:[{release_id:1,title:item.title,ok:true,already_exists:false}],added:1,failed:0,skipped:0};}
    else if(path==='/api/search')data={items:[{title:item.title,type:'movie',imdbid:item.imdb_id,tmdbid:item.tmdb_id}]};
    else if(path==='/api/activity')data={items:[],total:0};
    else if(path.endsWith('/options'))data={root_folders:[{id:1,path:'/media/movies'}],quality_profiles:[{id:7,name:'HD test profile'}]};
    else if(path.endsWith('/test')||path==='/api/sync')data={ok:true};
    else {status=404;data={error:'Unexpected fixture route '+path};}
    await route.fulfill({status,contentType:'application/json',body:JSON.stringify(data)});
  });
  await page.goto(origin+hash);
  return {page,requests,errors};
}

test('release detail supports verified monitoring and explicit single acquisition',async()=>{
  const {page,requests,errors}=await app({withRelease:true,configuredArr:true,hash:'#release/1'});
  try {
    const detail=page.locator('dialog[data-release-id="1"]');
    await detail.getByRole('heading',{name:'Handler test title',exact:true}).waitFor();
    await detail.getByText('Content advisories — original source fields',{exact:true}).click();
    await detail.getByText('Source supplied',{exact:false}).waitFor();
    await detail.locator('[data-action=monitor]').click();
    await detail.locator('[data-action=monitor][aria-pressed=true]').waitFor();
    await detail.locator('[data-action=acquire]').click();
    const confirm=page.getByRole('dialog',{name:'Add to Radarr?'});
    await confirm.waitFor();
    assert.equal(requests.some(r=>r.path.endsWith('/acquire')),false);
    assert.equal(await confirm.locator('[name=search]').isChecked(),false);
    await confirm.getByRole('button',{name:'Confirm add',exact:true}).click();
    await confirm.getByText('Added and verified in Radarr.',{exact:true}).waitFor();
    assert.deepEqual(requests.find(r=>r.path==='/api/releases/1/acquire').body,{target:'radarr',search:false,quality_profile_id:7,root_folder:'/media/movies'});
    await confirm.getByRole('button',{name:'Close dialog',exact:true}).click();
    await confirm.waitFor({state:'detached'});
    await page.keyboard.press('Escape');
    await detail.waitFor({state:'detached'});
    assert.deepEqual(errors,[]);
  } finally {await page.close();}
});

test('catalogue search adds only selected source identity and verifies readback',async()=>{
  const {page,requests,errors}=await app();
  try {
    await page.locator('.topbar [data-action=search]').last().click();
    const dialog=page.getByRole('dialog',{name:'Find a title'});
    await dialog.getByLabel('Title search',{exact:true}).fill('Handler');
    await dialog.getByRole('button',{name:'Search',exact:true}).click();
    await dialog.getByRole('button',{name:'Add to collection',exact:true}).click();
    await dialog.getByRole('button',{name:'View release',exact:true}).waitFor();
    assert.deepEqual(requests.find(r=>r.path==='/api/releases'&&r.method==='POST').body,{provider:'imdb',id:'tt7654321',type:'movie'});
    assert.equal(requests.filter(r=>r.path==='/api/releases/1'&&r.method==='GET').length,1);
    await page.keyboard.press('Escape');await dialog.waitFor({state:'detached'});
    assert.equal(await page.locator('.topbar [data-action=search]').last().evaluate(el=>el===document.activeElement),true);
    assert.deepEqual(errors,[]);
  } finally {await page.close();}
});

test('bulk acquisition confirms exact scope and renders every verified outcome',async()=>{
  const {page,requests,errors}=await app({withRelease:true,configuredArr:true,hash:'#/actresses'});
  try {
    await page.locator('.actress-card [data-action=actress]').first().click();
    const profile=page.getByRole('dialog',{name:'Actress watch',exact:true});
    await profile.getByRole('button',{name:'Add matched releases',exact:true}).click();
    const dialog=page.getByRole('dialog',{name:'Add matched releases?',exact:true});
    await dialog.getByText('1 matched release selected',{exact:true}).waitFor();
    assert.equal(requests.some(r=>r.path==='/api/actresses/2/acquire'),false);
    await dialog.getByRole('button',{name:'Confirm bulk add',exact:true}).click();
    await dialog.locator('.bulk-summary').getByText('1 added',{exact:true}).waitFor();
    await dialog.locator('.bulk-result').getByText('Added',{exact:true}).waitFor();
    assert.deepEqual(requests.find(r=>r.path==='/api/actresses/2/acquire').body,{scope:'all',search:false});
    assert.equal(await dialog.locator('.bulk-result').count(),1);
    await dialog.getByText('Local acquisition readback verified.',{exact:true}).waitFor();
    assert.deepEqual(errors,[]);
  } finally {await page.close();}
});

test('pending acquisition cannot be cancelled while an external write is running',async()=>{
  const {page}=await app({withRelease:true,configuredArr:true,hash:'#/release/1'});
  let releaseRequest;const gate=new Promise(resolve=>releaseRequest=resolve);
  await page.route('**/api/releases/1/acquire',async route=>{await gate;await route.fallback();});
  try {
    await page.locator('dialog[data-release-id="1"] [data-action=acquire]').click();
    const dialog=page.getByRole('dialog',{name:'Add to Radarr?'});
    await dialog.getByRole('button',{name:'Confirm add',exact:true}).click();
    assert.equal(await dialog.getByRole('button',{name:'Cancel',exact:true}).isDisabled(),true);
    await page.keyboard.press('Escape');assert.equal(await dialog.isVisible(),true);
    releaseRequest();await dialog.getByText('Added and verified in Radarr.',{exact:true}).waitFor();
  } finally {releaseRequest();await page.close();}
});

test('setup, logout and subsequent login verify the session and clear password fields',async()=>{
  const {page,requests,errors}=await app({authenticated:false,needsSetup:true});
  try {
    await page.locator('#auth-password').fill('test-only-password');await page.locator('#auth-confirm').fill('not-the-same-password');await page.locator('#auth-submit').click();
    await page.locator('#auth-error').getByText('The passwords don’t match.',{exact:true}).waitFor();
    assert.equal(requests.some(r=>r.path==='/api/setup'),false);
    await page.locator('#auth-confirm').fill('test-only-password');await page.locator('#auth-submit').click();
    await page.getByRole('heading',{name:'Your release radar.',exact:true}).waitFor();
    await page.locator('.nav-logout').click();await page.getByRole('heading',{name:'Welcome back.',exact:true}).waitFor();
    assert.equal(await page.locator('#auth-password').inputValue(),'');
    await page.locator('#auth-password').fill('test-only-password');await page.locator('#auth-submit').click();
    await page.getByRole('heading',{name:'Your release radar.',exact:true}).waitFor();
    assert.equal(requests.some(r=>r.path==='/api/login'),true);assert.deepEqual(errors,[]);
  }finally{await page.close();}
});

test('unsaved settings require discard confirmation and Pushover test requires explicit confirmation',async()=>{
  const {page,requests,errors}=await app({hash:'#/settings'});
  try {
    await page.locator('[name="public_url"]').fill('http://unsaved.test');await page.locator('[data-nav=activity]').click();
    const confirmation=page.getByRole('dialog',{name:'Leave unsaved settings?'});await confirmation.getByRole('button',{name:'Cancel',exact:true}).click();await confirmation.waitFor({state:'detached'});
    assert.equal(await page.locator('[name="public_url"]').inputValue(),'http://unsaved.test');
    await page.locator('[data-nav=activity]').click();await page.getByRole('button',{name:'Discard changes',exact:true}).click();
    await page.getByRole('heading',{name:'Behind the scenes.',exact:true}).waitFor();await page.getByRole('button',{name:'Sync now',exact:true}).click();
    assert.equal(requests.some(r=>r.path==='/api/sync'&&r.method==='POST'),true);
    await page.locator('[data-nav=settings]').click();await page.locator('[data-kind=pushover]').click();
    const push=page.getByRole('dialog',{name:'Send a test notification?'});await push.waitFor();assert.equal(requests.some(r=>r.path==='/api/integrations/pushover/test'),false);
    await push.getByRole('button',{name:'Send one notification',exact:true}).click();await page.locator('#pushover-result').getByText('One test notification sent.',{exact:true}).waitFor();
    assert.equal(requests.filter(r=>r.path==='/api/integrations/pushover/test').length,1);assert.deepEqual(errors,[]);
  }finally{await page.close();}
});

test('bulk failure remains visible per item instead of reporting a silent success',async()=>{
  const {page,errors}=await app({withRelease:true,hash:'#/actresses'});
  await page.route('**/api/actresses/2/acquire',route=>route.fulfill({contentType:'application/json',body:JSON.stringify({results:[{release_id:1,title:'Handler test title',ok:false,error:'Radarr is not configured'}],added:0,failed:1,skipped:0})}));
  try {
    await page.locator('.actress-card [data-action=actress]').first().click();await page.getByRole('button',{name:'Add matched releases',exact:true}).click();
    const dialog=page.getByRole('dialog',{name:'Add matched releases?',exact:true});await dialog.getByRole('button',{name:'Confirm bulk add',exact:true}).click();
    await dialog.locator('.bulk-summary').getByText('1 failed',{exact:true}).waitFor();await dialog.locator('.bulk-result.failed').getByText('Local release ID 1 · Radarr is not configured',{exact:true}).waitFor();assert.deepEqual(errors,[]);
  }finally{await page.close();}
});

test('failed acquisition preparation and actress detail requests stop loaders and retry locally',async()=>{
  const {page,errors}=await app({withRelease:true,configuredArr:true,hash:'#/releases'});
  let settingsFailure=true,personFailure=true;
  await page.route('**/api/settings',async route=>{if(settingsFailure){settingsFailure=false;await route.fulfill({status:502,contentType:'application/json',body:JSON.stringify({error:'Test integration unavailable'})});}else await route.fallback();});
  await page.route('**/api/actresses/2',async route=>{if(personFailure){personFailure=false;await route.fulfill({status:502,contentType:'application/json',body:JSON.stringify({error:'Test profile unavailable'})});}else await route.fallback();});
  try {
    await page.locator('.release-card [data-action=acquire]').click();
    let dialog=page.getByRole('dialog',{name:'Prepare acquisition'});await dialog.getByText('Test integration unavailable',{exact:true}).waitFor();assert.equal(await dialog.locator('.spinner').count(),0);
    await dialog.getByRole('button',{name:'Try again',exact:true}).click();dialog=page.getByRole('dialog',{name:'Add to Radarr?'});await dialog.getByRole('button',{name:'Confirm add',exact:true}).waitFor();await page.keyboard.press('Escape');await dialog.waitFor({state:'detached'});
    await page.locator('[data-nav=actresses]').click();await page.locator('.actress-card [data-action=actress]').first().click();
    dialog=page.getByRole('dialog',{name:'Actress watch',exact:true});await dialog.getByText('Test profile unavailable',{exact:true}).waitFor();await dialog.getByRole('button',{name:'Try again',exact:true}).click();await page.getByRole('dialog',{name:'Actress watch',exact:true}).getByRole('heading',{name:'Handler test watch',exact:true}).waitFor();assert.deepEqual(errors,[]);
  }finally{await page.close();}
});

test('browser initializes authenticated session and all six navigation views',async()=>{
  const {page,errors}=await app();
  try {
    await page.getByRole('heading',{name:'Your release radar.',exact:true}).waitFor();
    for(const [route,title] of [['releases','The collection.'],['calendar','Mark the moment.'],['actresses','Follow the people.'],['activity','Behind the scenes.'],['settings','Make it yours.']]) {
      await page.locator(`[data-nav=${route}]`).click();
      await page.getByRole('heading',{name:title,exact:true}).waitFor();
    }
    await page.locator('[name="radarr.url"]').fill('http://arr.test');
    await page.locator('[name="radarr.api_key"]').fill('test-only-secret');
    await page.getByRole('button',{name:'Save settings',exact:true}).click();
    await page.getByText('All changes saved.',{exact:true}).waitFor();
    assert.equal(await page.locator('[name="radarr.api_key"]').inputValue(),'');
    await page.locator('[data-action="integration-options"][data-kind="radarr"]').click();
    await page.locator('[name="radarr.quality_profile_id"]').selectOption('7');
    await page.locator('[name="radarr.root_folder"]').selectOption('/media/movies');
    await page.getByRole('button',{name:'Save settings',exact:true}).click();
    await page.getByText('All changes saved.',{exact:true}).waitFor();
    await page.locator('[data-action="integration-test"][data-kind="radarr"]').click();
    await page.locator('#radarr-result').getByText('Connection verified.',{exact:true}).waitFor();
    assert.deepEqual(errors,[]);
  } finally {await page.close();}
});
