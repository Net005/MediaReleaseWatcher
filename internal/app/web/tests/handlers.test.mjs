import test, { before, after } from 'node:test';
import assert from 'node:assert/strict';
import { readFile, readdir, access } from 'node:fs/promises';
import { createServer } from 'node:http';
import { chromium } from '@playwright/test';

let browser, server, origin;
const assets = new Map(await Promise.all(['index.html','app.js','styles.css'].map(async name => [name, await readFile(new URL('../'+name, import.meta.url),'utf8')])));
const defaults = () => ({silo:{url:'',configured:false,enabled:false},mdblist:{configured:false,list_ids:'',sync_interval_minutes:360},sonarr:{url:'',configured:false,quality_profile_id:0,root_folder:'',enabled:false,search:false},radarr:{url:'',configured:false,quality_profile_id:0,root_folder:'',enabled:false,search:false},pushover:{configured:false,enabled:false,device:''},automation:{enabled:false,notify_first_sync:false},public_url:''});
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
async function app({authenticated=true,needsSetup=false,withRelease=false,configuredArr=false,hash='',handle,autoScroll=false,artworkSVG}={}) {
  const page=await browser.newPage();page.setDefaultTimeout(4000);
  if(!autoScroll)await page.addInitScript(()=>{window.IntersectionObserver=undefined;});
  const requests=[],errors=[];page.on('pageerror',error=>errors.push(error.message));
  let settings=defaults(),item=release(),person=actress();
  if(configuredArr)settings.radarr={url:'http://arr.test',configured:true,enabled:true,search:true,root_folder:'/media/movies',quality_profile_id:7};
  await page.route('**/api/**',async route=>{
    const request=route.request(),url=new URL(request.url()),path=url.pathname,method=request.method();const body=request.postDataJSON();requests.push({path,method,body,query:Object.fromEntries(url.searchParams)});
    if(path==='/api/artwork'&&artworkSVG){await route.fulfill({status:200,contentType:'image/svg+xml',body:artworkSVG});return;}
    const custom=await handle?.({path,method,body,query:Object.fromEntries(url.searchParams)});
    if(custom){await route.fulfill({status:custom.status||200,contentType:'application/json',body:JSON.stringify(custom.data)});return;}
    let data={ok:true},status=200;
    if(path==='/api/mdblist/lists')data={items:settings.mdblist.lists||[]};
    else if(path==='/api/tasks')data={items:[]};
    else if(path==='/api/silo/sync'||path==='/api/arr/sync'){data={ok:true};status=202;}
    else if(path==='/api/releases/1/cast')data={items:[],coverage:'Source coverage unknown'};
    else if(path==='/api/session')data={authenticated,needs_setup:needsSetup};
    else if(path==='/api/login'||path==='/api/setup'){authenticated=true;needsSetup=false;data={authenticated:true};}
    else if(path==='/api/logout')authenticated=false;
    else if(path==='/api/dashboard')data={stats:{releases:withRelease?1:0,actresses:1,upcoming:0,current:0,past:withRelease?1:0,monitored:0},featured:[],upcoming:[],recent:[],activity:[],sync:{running:false}};
    else if(path==='/api/settings'){if(method==='PUT'){const old=settings;settings=structuredClone(body);for(const kind of ['mdblist','sonarr','radarr','pushover','silo']){settings[kind].configured=old[kind].configured||Boolean(body[kind].api_key||body[kind].token);delete settings[kind].api_key;delete settings[kind].token;delete settings[kind].user;}}data=settings;}
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

test('actress upcoming-only filter uses the API, persists across navigation and never narrows group cache',async()=>{
  const {page,requests,errors}=await app({hash:'#/actresses',handle:({path,query})=>path==='/api/actresses'?{data:{items:query.upcoming_only==='true'?[]:[actress(),{...actress(),id:3,tmdb_id:'123457',group:'Synthetic group'}],total:query.upcoming_only==='true'?0:2}}:undefined});
  try {
    const upcoming=page.getByLabel('Upcoming releases only',{exact:true});await upcoming.check();
    await page.getByText('No watches have upcoming releases.',{exact:true}).waitFor();
    assert.equal(requests.filter(r=>r.path==='/api/actresses').at(-1).query.upcoming_only,'true');
    assert.equal(await page.locator('#actress-group option[value="Synthetic group"]').count(),1);
    await page.locator('[data-nav=releases]').click();await page.getByRole('heading',{name:'The collection.',exact:true}).waitFor();await page.locator('[data-nav=actresses]').click();
    assert.equal(await upcoming.isChecked(),true);await upcoming.uncheck();await page.locator('.actress-card').nth(1).waitFor();
    assert.equal(requests.filter(r=>r.path==='/api/actresses').at(-1).query.upcoming_only,undefined);assert.deepEqual(errors,[]);
  }finally{await page.close();}
});

test('bulk watch import visibly reuses single-add fields and sends defaults without altering entry flags or groups',async()=>{
  const entries=[{name:'Synthetic one',actor_id:'000123'},{name:'Synthetic two',actor_id:'000124',group:'Original group',monitored:false,auto_add:false,notify:true},{name:'Synthetic one duplicate',actor_id:'000123'}];
  let imported=false;
  const {page,requests,errors}=await app({hash:'#/actresses',handle:({path,method})=>{
    if(path==='/api/actresses/import'&&method==='POST'){imported=true;return {data:{ok:true}};}
    if(path==='/api/actresses'&&imported)return {data:{items:[{...actress(),tmdb_id:'000123'},{...actress(),id:3,tmdb_id:'000124'}],total:2}};
  }});
  try {
    await page.getByRole('button',{name:'Add watch',exact:true}).click();let dialog=page.getByRole('dialog',{name:'Add an actress watch',exact:true});
    const singleFields=await dialog.locator('[name=group],[name=monitored],[name=auto_add],[name=notify]').evaluateAll(nodes=>nodes.map(node=>({name:node.name,type:node.type,checked:node.checked||false})));
    await dialog.getByRole('button',{name:'Cancel',exact:true}).click();await page.getByRole('button',{name:'Import',exact:true}).click();dialog=page.getByRole('dialog',{name:'Import actress watches',exact:true});
    assert.deepEqual(await dialog.locator('[name=group],[name=monitored],[name=auto_add],[name=notify]').evaluateAll(nodes=>nodes.map(node=>({name:node.name,type:node.type,checked:node.checked||false}))),singleFields);
    await dialog.getByText('Explicit entry flags and entry groups win; original groups mappings are preserved.',{exact:false}).waitFor();
    await dialog.getByLabel('Group',{exact:true}).fill('Default synthetic group');await dialog.getByLabel('Monitor this actress',{exact:true}).uncheck();await dialog.getByLabel('Automatically add future matched releases',{exact:true}).check();
    await dialog.getByLabel('Watch list JSON',{exact:true}).fill(JSON.stringify({entries,groups:{'Original group':['000124']}}));
    await dialog.getByRole('button',{name:'Import watches',exact:true}).click();await dialog.waitFor({state:'detached'});
    assert.deepEqual(requests.find(r=>r.path==='/api/actresses/import').body,{entries,groups:{'Original group':['000124']},group:'Default synthetic group',monitored:false,auto_add:true,notify:false});assert.deepEqual(errors,[]);
  }finally{await page.close();}
});

test('MDBList watchlist unknown retries read-only then confirms add and removal with verified readbacks',async()=>{
  let status='unknown',value=null;
  const {page,requests,errors}=await app({withRelease:true,hash:'#/release/1',handle:({path,method,body})=>{
    if(path==='/api/releases/1/watchlist'){
      if(method==='POST'){value=body.in_watchlist;status=value?'present':'absent';return {data:{status,in_watchlist:value,verified:true}};}
      return {data:{status,in_watchlist:value,error:status==='unknown'?'Synthetic provider unavailable':''}};
    }
  }});
  try {
    const detail=page.locator('dialog[data-release-id="1"]');await detail.getByText('MDBList watchlist status unknown',{exact:true}).waitFor();
    assert.equal(requests.some(r=>r.path.endsWith('/watchlist')&&r.method==='POST'),false);assert.equal(await detail.getByRole('button',{name:'Add to MDBList watchlist',exact:true}).count(),0);
    status='absent';value=false;await detail.getByRole('button',{name:'Retry watchlist status',exact:true}).click();await detail.getByText('Not in MDBList watchlist',{exact:true}).waitFor();
    await detail.getByRole('button',{name:'Add to MDBList watchlist',exact:true}).click();const add=page.getByRole('dialog',{name:'Add to MDBList watchlist?',exact:true});
    assert.equal(requests.some(r=>r.path.endsWith('/watchlist')&&r.method==='POST'),false);await add.getByRole('button',{name:'Confirm watchlist add',exact:true}).click();
    await detail.getByText('In MDBList watchlist',{exact:true}).waitFor();assert.deepEqual(requests.filter(r=>r.method==='POST'&&r.path.endsWith('/watchlist')).map(r=>r.body),[{in_watchlist:true}]);
    assert.equal(requests.filter(r=>r.path.endsWith('/watchlist')).at(-1).method,'GET');
    await detail.getByRole('button',{name:'Remove from MDBList watchlist',exact:true}).click();await page.getByRole('button',{name:'Confirm watchlist removal',exact:true}).click();await detail.getByText('Not in MDBList watchlist',{exact:true}).waitFor();
    assert.deepEqual(requests.filter(r=>r.method==='POST'&&r.path.endsWith('/watchlist')).map(r=>r.body),[{in_watchlist:true},{in_watchlist:false}]);assert.deepEqual(errors,[]);
  }finally{await page.close();}
});

test('unverified watchlist writes stay unknown and retry reads without repeating the write',async()=>{
  let unverified=false;
  const {page,requests,errors}=await app({withRelease:true,hash:'#/release/1',handle:({path,method})=>{
    if(path==='/api/releases/1/watchlist'){
      if(method==='POST'){unverified=true;return {data:{status:'present',in_watchlist:true,verified:false}};}
      return {data:{status:unverified?'present':'absent',in_watchlist:unverified}};
    }
  }});
  try {
    const detail=page.locator('dialog[data-release-id="1"]');await detail.getByRole('button',{name:'Add to MDBList watchlist',exact:true}).click();await page.getByRole('button',{name:'Confirm watchlist add',exact:true}).click();
    await detail.getByText('MDBList watchlist status unknown',{exact:true}).waitFor();await detail.getByText('The server did not verify the watchlist change. Retry the status check before taking another action.',{exact:true}).waitFor();
    await detail.getByRole('button',{name:'Retry watchlist status',exact:true}).click();await detail.getByText('In MDBList watchlist',{exact:true}).waitFor();assert.equal(requests.filter(r=>r.path.endsWith('/watchlist')&&r.method==='POST').length,1);assert.deepEqual(errors,[]);
  }finally{await page.close();}
});

test('scheduled tasks create and edit recurring Arr inventory, verify toggles, run now and delete',async()=>{
  let tasks=[];
  const {page,requests,errors}=await app({hash:'#/settings',handle:({path,method,body})=>{
    if(path==='/api/tasks'){
      if(method==='POST'){const task={...body,id:41,next_run:'2026-10-19T07:00:00Z',last_run:null,last_status:'never',last_error:''};tasks.push(task);return {status:201,data:task};}
      return {data:{items:tasks}};
    }
    if(path==='/api/tasks/41'){
      if(method==='PATCH')tasks[0]={...tasks[0],...body};if(method==='DELETE')tasks=[];return {data:tasks[0]||{ok:true}};
    }
    if(path==='/api/tasks/41/run'){tasks[0]={...tasks[0],last_run:'2026-10-18T12:01:00Z',last_status:'running'};return {status:202,data:{ok:true}};}
  }});
  try {
    await page.getByRole('button',{name:'Add scheduled task',exact:true}).click();let dialog=page.getByRole('dialog',{name:'Add scheduled task',exact:true});
    await dialog.getByLabel('Task name',{exact:true}).fill('Synthetic inventory');await dialog.getByLabel('Task type',{exact:true}).selectOption('arr_sync');await dialog.getByLabel('Recurrence',{exact:true}).selectOption('weekly');
    await dialog.getByLabel('Start date',{exact:true}).fill('2026-10-19');await dialog.getByLabel('Time',{exact:true}).fill('09:00');await dialog.getByLabel('Timezone (IANA)',{exact:true}).fill('Europe/Amsterdam');await dialog.getByLabel('Monday',{exact:true}).check();await dialog.getByLabel('Friday',{exact:true}).check();
    assert.equal(await dialog.getByLabel('Enable schedule',{exact:true}).isChecked(),false);await dialog.getByRole('button',{name:'Save task',exact:true}).click();await dialog.waitFor({state:'detached'});
    let row=page.locator('[data-task-id="41"]');await row.getByText('Synthetic inventory',{exact:true}).waitFor();await row.getByText('Never run',{exact:true}).waitFor();
    const created=requests.find(r=>r.path==='/api/tasks'&&r.method==='POST').body;assert.deepEqual(created,{name:'Synthetic inventory',type:'arr_sync',enabled:false,frequency:'weekly',date:'2026-10-19',time:'09:00',timezone:'Europe/Amsterdam',interval_minutes:60,weekdays:[1,5],allow_download_search:false});
    await row.getByRole('button',{name:'Edit task',exact:true}).click();dialog=page.getByRole('dialog',{name:'Edit scheduled task',exact:true});await dialog.getByLabel('Task name',{exact:true}).fill('Updated inventory');await dialog.getByLabel('Recurrence',{exact:true}).selectOption('interval');await dialog.getByLabel('Interval (minutes)',{exact:true}).fill('120');await dialog.getByRole('button',{name:'Save task',exact:true}).click();await dialog.waitFor({state:'detached'});await row.getByText('Updated inventory',{exact:true}).waitFor();
    await row.getByRole('button',{name:'Enable task',exact:true}).click();await row.getByRole('button',{name:'Disable task',exact:true}).waitFor();assert.equal(tasks[0].enabled,true);
    await row.getByRole('button',{name:'Run now',exact:true}).click();await row.getByText('running',{exact:true}).waitFor();assert.equal(requests.filter(r=>r.path==='/api/tasks/41/run').at(-1).method,'POST');assert.equal(requests.filter(r=>r.path.startsWith('/api/tasks')).at(-1).method,'GET');
    tasks[0].last_status='ok';await page.getByRole('button',{name:'Refresh scheduled tasks',exact:true}).click();await row.getByText('ok',{exact:true}).waitFor();
    await row.getByRole('button',{name:'Delete task',exact:true}).click();await page.getByRole('button',{name:'Delete scheduled task',exact:true}).click();await page.getByText('No scheduled tasks yet.',{exact:true}).waitFor();assert.deepEqual(errors,[]);
  }finally{await page.close();}
});

test('Arr inventory and readiness badges remain independent from Silo and failures never imply absence',async()=>{
  const items=[
    {...release(),id:1,acquisition:{radarr:'already_exists',radarr_status:'present',radarr_ready:'false',radarr_file_count:'0',radarr_url:'https://arr.test/movie/1'},local_availability:{status:'unknown'}},
    {...release(),id:2,type:'show',acquisition:{sonarr:'already_exists',sonarr_status:'present',sonarr_ready:'true',sonarr_file_count:'5',sonarr_url:'javascript:alert(1)'},local_availability:{status:'absent'}},
    {...release(),id:3,acquisition:{radarr_status:'absent'}},
    {...release(),id:4,acquisition:{radarr:'already_exists',radarr_status:'present',radarr_error:'Synthetic inventory check failed'}}
  ];
  const {page,errors}=await app({hash:'#/releases',handle:({path})=>path==='/api/releases'?{data:{items,total:4,page:1,limit:36}}:undefined});
  try {
    const cards=page.locator('.release-card');await cards.nth(3).waitFor();await cards.nth(0).getByText('Radarr inventory: present',{exact:true}).waitFor();await cards.nth(0).getByText('Radarr readiness: not ready',{exact:true}).waitFor();await cards.nth(1).getByText('Sonarr readiness: ready · 5 files',{exact:true}).waitFor();
    await cards.nth(2).getByText('Radarr inventory: absent',{exact:true}).waitFor();await cards.nth(3).getByText('Radarr inventory: unknown',{exact:true}).waitFor();await cards.nth(3).getByText('Synthetic inventory check failed',{exact:true}).waitFor();
    assert.equal(await page.getByText('Local file available',{exact:true}).count(),0);assert.equal(await page.getByRole('link',{name:'Open Radarr',exact:true}).getAttribute('href'),'https://arr.test/movie/1');assert.equal(await page.getByRole('link',{name:'Open Sonarr',exact:true}).count(),0);assert.deepEqual(errors,[]);
  }finally{await page.close();}
});

test('upgraded settings, cast and import layouts stay aligned and responsive',async()=>{
  const {page,errors}=await app({withRelease:true,hash:'#/settings',handle:({path})=>path==='/api/releases/1/cast'?{data:{items:[{tmdb_id:'123',name:'Synthetic cast person',character:'Source role',profile:''}],coverage:'Source supplied'}}:undefined});
  try {
    await page.setViewportSize({width:1360,height:960});await page.locator('#task-list').waitFor();
    assert.equal(await page.locator('.task-settings').evaluate(node=>getComputedStyle(node).gridColumnStart),'2');
    const form=await page.locator('#settings-form').boundingBox(),tasks=await page.locator('#settings-schedules').boundingBox();assert.equal(Math.round(form.x),Math.round(tasks.x));
    await page.setViewportSize({width:390,height:844});assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
    await page.locator('[data-nav=releases]').click();await page.locator('.release-card [data-action=release]').first().click();const detail=page.locator('dialog[data-release-id="1"]');await detail.locator('.cast-card').waitFor();
    assert.equal(await detail.locator('.cast-grid').evaluate(node=>getComputedStyle(node).display),'grid');assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
    if(process.env.MRW_SCREENSHOTS_DIR)await page.screenshot({path:process.env.MRW_SCREENSHOTS_DIR+'/release-mobile.png',fullPage:true});
    await page.keyboard.press('Escape');await page.locator('[data-nav=actresses]').click();await page.getByRole('button',{name:'Import',exact:true}).click();await page.getByRole('dialog',{name:'Import actress watches',exact:true}).getByLabel('Notify on new matched releases',{exact:true}).waitFor();
    assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);if(process.env.MRW_SCREENSHOTS_DIR)await page.screenshot({path:process.env.MRW_SCREENSHOTS_DIR+'/import-mobile.png',fullPage:true});assert.deepEqual(errors,[]);
  }finally{await page.close();}
});

test('download-search tasks require per-task consent, saved connections and a separate run confirmation',async()=>{
  let tasks=[];
  const {page,requests,errors}=await app({hash:'#/settings',handle:({path,method,body})=>{
    if(path==='/api/tasks'){
      if(method==='POST'){tasks=[{...body,id:51,last_run:null,last_status:'',last_error:''}];return {status:201,data:tasks[0]};}
      return {data:{items:tasks}};
    }
    if(path==='/api/tasks/51/run'){tasks[0]={...tasks[0],last_run:'2026-10-18T15:02:00Z',last_status:'running'};return {status:202,data:{ok:true}};}
  }});
  try {
    await page.getByRole('button',{name:'Add scheduled task',exact:true}).click();const dialog=page.getByRole('dialog',{name:'Add scheduled task',exact:true});
    await dialog.getByLabel('Task name',{exact:true}).fill('Synthetic monitored search');await dialog.getByLabel('Task type',{exact:true}).selectOption('search_missing');await dialog.getByLabel('Enable schedule',{exact:true}).check();
    assert.equal(await dialog.getByLabel('Allow download searches for app-monitored missing releases',{exact:true}).isChecked(),false);await dialog.getByRole('button',{name:'Save task',exact:true}).click();await dialog.getByText('Enabled search tasks require explicit download-search consent.',{exact:true}).waitFor();assert.equal(requests.some(r=>r.path==='/api/tasks'&&r.method==='POST'),false);
    await dialog.getByLabel('Allow download searches for app-monitored missing releases',{exact:true}).check();await dialog.getByRole('button',{name:'Save task',exact:true}).click();await dialog.waitFor({state:'detached'});assert.equal(requests.find(r=>r.path==='/api/tasks'&&r.method==='POST').body.allow_download_search,true);
    const row=page.locator('[data-task-id="51"]');await row.getByRole('button',{name:'Run now',exact:true}).click();let confirm=page.getByRole('dialog',{name:'Run download searches now?',exact:true});await confirm.waitFor();assert.equal(requests.some(r=>r.path.endsWith('/51/run')),false);await confirm.getByRole('button',{name:'Cancel',exact:true}).click();
    await page.locator('[name="public_url"]').fill('https://unsaved.test');await row.getByRole('button',{name:'Run now',exact:true}).click();await page.getByText('Save or reset settings before running tasks against saved connections.',{exact:true}).waitFor();assert.equal(requests.some(r=>r.path.endsWith('/51/run')),false);
    await page.getByRole('button',{name:'Reset changes',exact:true}).click();await page.getByRole('button',{name:'Reset changes',exact:true}).last().click();await row.getByRole('button',{name:'Run now',exact:true}).click();confirm=page.getByRole('dialog',{name:'Run download searches now?',exact:true});await confirm.getByRole('button',{name:'Run monitored searches',exact:true}).click();await row.getByText('running',{exact:true}).waitFor();assert.equal(requests.filter(r=>r.path.endsWith('/51/run')).length,1);assert.deepEqual(errors,[]);
  }finally{await page.close();}
});

test('scheduled-task errors and mismatched readbacks remain visible instead of claiming success',async()=>{
  let fail=true;
  const {page,requests,errors}=await app({hash:'#/settings',handle:({path,method})=>{
    if(path==='/api/tasks'){
      if(method==='POST')return {status:201,data:{id:61}};
      if(fail){fail=false;return {status:502,data:{error:'Synthetic scheduler unavailable'}};}
      return {data:{items:[]}};
    }
  }});
  try {
    await page.getByText('Synthetic scheduler unavailable',{exact:true}).waitFor();await page.getByRole('button',{name:'Retry scheduled tasks',exact:true}).click();await page.getByText('No scheduled tasks yet.',{exact:true}).waitFor();
    await page.getByRole('button',{name:'Add scheduled task',exact:true}).click();const dialog=page.getByRole('dialog',{name:'Add scheduled task',exact:true});await dialog.getByLabel('Task name',{exact:true}).fill('Unpersisted task');await dialog.getByRole('button',{name:'Save task',exact:true}).click();await dialog.getByText('Task readback did not match the requested settings. Refresh scheduled tasks before retrying.',{exact:true}).waitFor();assert.equal(await dialog.isVisible(),true);assert.equal(requests.filter(r=>r.path==='/api/tasks'&&r.method==='POST').length,1);assert.deepEqual(errors,[]);
  }finally{await page.close();}
});

test('saved list policies immediately update release filters and preserve selection across navigation',async()=>{
  const {page,requests,errors}=await app({hash:'#/settings',withRelease:true});
  try {
    await page.getByRole('button',{name:'Add personal list',exact:true}).click();const row=page.locator('.list-policy-row');await row.getByLabel('List ID',{exact:true}).fill('00042');await row.getByLabel('List name',{exact:true}).fill('New synthetic list');await row.getByLabel('Enabled',{exact:true}).check();await row.getByLabel('Monitor',{exact:true}).check();
    await page.getByRole('button',{name:'Save settings',exact:true}).click();await page.getByText('All changes saved.',{exact:true}).waitFor();await page.locator('[data-nav=releases]').click();await page.getByLabel('Filter by personal list',{exact:true}).selectOption('00042');await page.locator('.release-card').waitFor();
    await page.getByLabel('Media type',{exact:true}).selectOption('movie');await page.locator('.release-card').waitFor();await page.locator('[data-nav=actresses]').click();await page.getByRole('heading',{name:'Follow the people.',exact:true}).waitFor();await page.locator('[data-nav=releases]').click();await page.locator('.release-card').waitFor();
    assert.equal(await page.getByLabel('Filter by personal list',{exact:true}).inputValue(),'00042');assert.equal(await page.getByLabel('Media type',{exact:true}).inputValue(),'movie');assert.equal(requests.filter(r=>r.path==='/api/releases'&&r.method==='GET').at(-1).query.list_id,'00042');assert.deepEqual(errors,[]);
  }finally{await page.close();}
});

test('personal list settings start opted out and verify independent policies after save',async()=>{
  const {page,requests,errors}=await app({hash:'#/settings'});
  try {
    await page.getByRole('button',{name:'Add personal list',exact:true}).click();
    const row=page.locator('.list-policy-row');
    for(const name of ['Enabled','Monitor','Auto-add'])assert.equal(await row.getByLabel(name,{exact:true}).isChecked(),false);
    await row.getByLabel('List ID',{exact:true}).fill('0012');await row.getByLabel('List name',{exact:true}).fill('Personal synthetic list');
    await row.getByLabel('Enabled',{exact:true}).check();
    await page.getByRole('button',{name:'Save settings',exact:true}).click();
    await page.getByText('All changes saved.',{exact:true}).waitFor();
    assert.deepEqual(requests.find(r=>r.path==='/api/settings'&&r.method==='PUT').body.mdblist.lists,[{id:'0012',name:'Personal synthetic list',enabled:true,monitored:false,auto_add:false}]);
    assert.equal(await page.locator('.list-policy-row').getByLabel('List ID',{exact:true}).inputValue(),'0012');assert.deepEqual(errors,[]);
  }finally{await page.close();}
});

test('release scrolling appends unique titles, retries page errors, ends and resets personal-list filters',async()=>{
  let fail=true;
  const {page,requests,errors}=await app({hash:'#/releases',handle:({path,query})=>{
    if(path==='/api/mdblist/lists')return {data:{items:[{id:'0012',name:'Personal synthetic list'}]}};
    if(path==='/api/releases'){
      const p=Number(query.page);if(p===2&&fail){fail=false;return {status:502,data:{error:'Synthetic page unavailable'}};}
      const ids=query.list_id?[9]:p===1?[1,2]:[2,3];
      return {data:{items:ids.map(id=>({...release(),id,title:`Release ${id}`})),page:p,limit:2,total:query.list_id?1:4}};
    }
  }});
  try {
    await page.locator('.release-card').nth(1).waitFor();
    await page.getByRole('button',{name:'Load more releases',exact:true}).click();
    await page.getByText('Synthetic page unavailable',{exact:true}).waitFor();assert.equal(await page.locator('.release-card').count(),2);
    await page.getByRole('button',{name:'Try loading more',exact:true}).click();
    await page.getByText('All matching releases loaded.',{exact:true}).waitFor();
    assert.equal(await page.locator('.release-card').count(),3);
    await page.getByLabel('Filter by personal list',{exact:true}).selectOption('0012');
    await page.getByRole('button',{name:'View Release 9',exact:true}).waitFor();assert.equal(await page.locator('.release-card').count(),1);
    assert.equal(requests.filter(r=>r.path==='/api/releases').at(-1).query.page,'1');assert.equal(requests.filter(r=>r.path==='/api/releases').at(-1).query.list_id,'0012');assert.deepEqual(errors,[]);
  }finally{await page.close();}
});

test('release query changes abort stale requests before debounce and never replace newer results',async()=>{
  const {page,errors}=await app({withRelease:true,hash:'#/releases'});let finish;
  const gate=new Promise(resolve=>finish=resolve);const aborted=[];page.on('requestfailed',r=>{if(r.url().includes('/api/releases?'))aborted.push(r.url());});
  await page.route('**/api/releases?*',async route=>{
    const query=new URL(route.request().url()).searchParams;if(query.get('q')==='old')await gate;
    await route.fulfill({contentType:'application/json',body:JSON.stringify({items:[{...release(),title:query.get('q')||'Initial'}],total:1,page:1,limit:36})});
  });
  try {
    await page.locator('.release-card').waitFor();await page.getByLabel('Search your releases',{exact:true}).fill('old');
    await page.waitForRequest(r=>new URL(r.url()).searchParams.get('q')==='old');
    await page.getByLabel('Search your releases',{exact:true}).fill('new');
    await page.getByRole('button',{name:'View new',exact:true}).waitFor();finish();
    assert.equal(aborted.some(url=>new URL(url).searchParams.get('q')==='old'),true);assert.equal(await page.getByRole('button',{name:'View old',exact:true}).count(),0);assert.deepEqual(errors,[]);
  }finally{finish();await page.close();}
});

test('intersection observer automatically loads the next release page on overview',async()=>{
  const {page,requests,errors}=await app({autoScroll:true,handle:({path,query})=>path==='/api/releases'?{data:{items:[{...release(),id:Number(query.page),title:'Scroll '+query.page}],total:2,page:Number(query.page),limit:1}}:undefined});
  try{await page.locator('#release-pagination').scrollIntoViewIfNeeded();await page.getByText('All matching releases loaded.',{exact:true}).waitFor();assert.equal(await page.locator('.release-card').count(),2);assert.equal(requests.filter(r=>r.path==='/api/releases').length,2);assert.deepEqual(errors,[]);}finally{await page.close();}
});

test('release cast opens source person details and explains list and actress monitoring separately from local files',async()=>{
  const {page,errors}=await app({withRelease:true,hash:'#/release/1',handle:({path})=>{
    if(path==='/api/releases/1')return {data:{...release(),monitoring_origin:'actress',monitored:true,lists:[{id:'0012',name:'Synthetic source',monitored:false,auto_add:false}],acquisition:{radarr:'already_exists'},local_availability:{status:'unknown',url:'javascript:alert(1)'}}};
    if(path==='/api/releases/1/cast')return {data:{items:[{tmdb_id:'00123',name:'Synthetic actor <script>',character:'A role',profile:''}],coverage:'Verified source credits'}};
    if(path==='/api/people/00123')return {data:{person:{tmdb_id:'00123',name:'Synthetic actor <script>',biography:'Source biography',metadata_status:'Verified biography'},releases:[release()]}};
  }});
  try {
    const detail=page.locator('dialog[data-release-id="1"]');await detail.getByText('Synthetic source',{exact:false}).waitFor();await detail.getByText('Actress watch rule (Auto-add / Notify)',{exact:true}).waitFor();
    await detail.getByRole('button',{name:'Monitor & search in Radarr',exact:true}).waitFor();await detail.getByText('Silo presence unknown',{exact:true}).waitFor();
    await detail.getByRole('button',{name:'View person Synthetic actor <script>',exact:true}).click();
    const person=page.getByRole('dialog',{name:'Person details',exact:true});await person.getByText('Source biography',{exact:true}).waitFor();await person.getByText('00123',{exact:true}).waitFor();assert.equal(await page.locator('script:not([src])').count(),0);assert.deepEqual(errors,[]);
  }finally{await page.close();}
});

test('Silo saves redacted credentials, tests saved connection and queues independent read-only Silo and Arr inventory syncs',async()=>{
  const {page,requests,errors}=await app({hash:'#/settings'});
  try {
    await page.getByLabel('Silo server URL',{exact:true}).fill('https://silo.test');await page.locator('[name="silo.api_key"]').fill('synthetic-only');await page.locator('[name="silo.enabled"]').check();
    await page.getByRole('button',{name:'Save settings',exact:true}).click();await page.getByText('All changes saved.',{exact:true}).waitFor();
    assert.equal(await page.locator('[name="silo.api_key"]').inputValue(),'');assert.deepEqual(requests.find(r=>r.method==='PUT').body.silo,{url:'https://silo.test',enabled:true,api_key:'synthetic-only'});
    await page.getByRole('button',{name:'Test Silo connection',exact:true}).click();await page.locator('#silo-result').getByText('Connection verified.',{exact:true}).waitFor();
    await page.getByRole('button',{name:'Sync Silo availability',exact:true}).click();await page.locator('#silo-sync-result').getByText('Silo availability sync queued. Check Activity for completion.',{exact:true}).waitFor();
    await page.getByRole('button',{name:'Sync Arr inventory',exact:true}).click();await page.locator('#arr-sync-result').getByText('Arr inventory sync queued. No download searches requested.',{exact:true}).waitFor();
    for(const path of ['/api/silo/sync','/api/arr/sync'])assert.deepEqual(requests.find(r=>r.path===path).body,{});
    assert.equal(requests.some(r=>r.path.includes('/acquire')),false);assert.deepEqual(errors,[]);
  }finally{await page.close();}
});

test('Silo catalog presence has a safe In Silo link in cards and detail readback without health claims',async()=>{
  const present={...release(),local_availability:{status:'available',url:'https://silo.test/base/item/movie:synthetic',checked_at:'2026-10-18T12:00:00Z'}};
  const {page,requests,errors}=await app({hash:'#/releases',handle:({path})=>{
    if(path==='/api/releases')return {data:{items:[present,{...present,id:2,local_availability:{...present.local_availability,url:'javascript:alert(1)'}}],total:2,page:1,limit:36}};
    if(path==='/api/releases/1')return {data:present};
  }});
  try {
    const first=page.locator('.release-card').first();await first.getByText('In Silo',{exact:true}).waitFor();
    assert.equal(await page.getByText(/playback|playable|local file available|file indexed/i).count(),0);
    assert.equal(await page.getByRole('link',{name:'Open in Silo',exact:true}).count(),1);assert.equal(await first.getByRole('link',{name:'Open in Silo',exact:true}).getAttribute('href'),present.local_availability.url);
    await first.getByRole('button',{name:'View Handler test title',exact:true}).click();const detail=page.locator('dialog[data-release-id="1"]');
    await detail.getByText('In Silo',{exact:true}).waitFor();
    assert.equal(await detail.getByRole('link',{name:'Open in Silo',exact:true}).getAttribute('href'),present.local_availability.url);
    assert.equal(requests.some(r=>r.path==='/api/releases/1'&&r.method==='GET'),true);assert.equal(requests.some(r=>r.path.includes('/acquire')),false);assert.deepEqual(errors,[]);
  }finally{await page.close();}
});

test('Silo presence badges reflect catalog lookup and safe links, not Arr presence',async()=>{
  const statuses=['available','absent','unknown','error'];
  const {page,errors}=await app({hash:'#/releases',handle:({path})=>path==='/api/releases'?{data:{items:statuses.map((status,index)=>({...release(),id:index+1,acquisition:{radarr:index===3?'failed':'already_exists'},local_availability:{status,url:index===0?'https://silo.test/item/1':'javascript:alert(1)',checked_at:'2026-10-18T12:00:00Z'}})),total:4,page:1,limit:36}}:undefined});
  try {
    await page.locator('.release-card').nth(3).waitFor();for(const [index,label] of ['In Silo','Not in Silo','Silo presence unknown','Silo lookup failed'].entries())await page.locator('.release-card').nth(index).getByText(label,{exact:true}).waitFor();
    assert.equal(await page.getByRole('link',{name:'Open in Silo',exact:true}).count(),1);assert.equal(await page.getByRole('link',{name:'Open in Silo',exact:true}).getAttribute('href'),'https://silo.test/item/1');assert.equal(await page.getByRole('button',{name:'Monitor & search in Radarr',exact:true}).count(),3);assert.deepEqual(errors,[]);
  }finally{await page.close();}
});

for(const [type,target,name] of [['movie','radarr','Radarr'],['show','sonarr','Sonarr']])test(`known unmonitored ${type} explicitly restores monitoring and searches`,async()=>{
  const item={...release(),type,acquisition:{[target]:'already_exists',[target+'_status']:'present',[target+'_ready']:'false'}};
  const {page,requests,errors}=await app({withRelease:true,hash:'#/release/1',handle:({path})=>{
    if(path==='/api/releases/1')return {data:item};
    if(path==='/api/settings')return {data:{...defaults(),[target]:{configured:true,enabled:true,quality_profile_id:7,root_folder:'/media/movies',search:false}}};
    if(path==='/api/releases/1/acquire')return {data:{ok:true,result:{id:11,already_exists:true,monitored_restored:true,search_started:true}}};
  }});
  try {
    const detail=page.locator('dialog[data-release-id="1"]');
    await detail.getByText(`${name} inventory: present`,{exact:true}).waitFor();
    await detail.getByRole('button',{name:`Monitor & search in ${name}`,exact:true}).click();
    const dialog=page.getByRole('dialog',{name:`Monitor & search in ${name}?`,exact:true});
    await dialog.getByText('Existing entries will be set to monitored, including all Sonarr seasons. New entries will also be monitored. A search will start and may download media.',{exact:true}).waitFor();
    assert.equal(await dialog.locator('[name=search]').isChecked(),true);assert.equal(await dialog.locator('[name=search]').isDisabled(),true);
    assert.equal(requests.some(r=>r.path.endsWith('/acquire')),false);
    await dialog.getByRole('button',{name:'Confirm monitor & search',exact:true}).click();
    await dialog.getByText('Monitoring restored. Search started.',{exact:true}).waitFor();
    assert.deepEqual(requests.find(r=>r.path.endsWith('/acquire')).body,{target,search:true,quality_profile_id:7,root_folder:'/media/movies'});assert.deepEqual(errors,[]);
  }finally{await page.close();}
});

test('single outcomes do not invent unconfirmed restoration or search success',async()=>{
  const {page,requests}=await app({withRelease:true,configuredArr:true,hash:'#/release/1',handle:({path})=>path==='/api/releases/1/acquire'?{data:{ok:true,result:{id:11,already_exists:true,monitored_restored:false,search_started:false}}}:path==='/api/releases/1'?{data:{...release(),acquisition:{radarr:'already_exists'}}}:undefined});
  try {
    await page.locator('dialog[data-release-id="1"] [data-action=acquire]').click();const dialog=page.getByRole('dialog',{name:'Monitor & search in Radarr?',exact:true});
    await dialog.getByRole('button',{name:'Confirm monitor & search',exact:true}).click();
    await dialog.getByText('Monitoring restoration not confirmed. Search start not confirmed. Check Activity before retrying.',{exact:true}).waitFor();
    assert.equal(await dialog.getByText('Search started.',{exact:true}).count(),0);assert.equal(requests.filter(r=>r.path.endsWith('/acquire')).length,1);
  }finally{await page.close();}
});

test('bulk existing outcomes expose restored monitoring and verified searches without changing counts',async()=>{
  const item={...release(),title:'<img src=x onerror=alert(1)>',acquisition:{radarr:'already_exists'}};
  const {page,requests,errors}=await app({withRelease:true,configuredArr:true,hash:'#/actresses',handle:({path})=>{
    if(path==='/api/actresses/2')return {data:{actress:actress(),releases:[item],total:1}};
    if(path==='/api/actresses/2/acquire')return {data:{results:[{release_id:1,title:item.title,ok:true,already_exists:true,monitored_restored:true,search_started:true}],added:0,failed:0,skipped:1}};
  }});
  try {
    await page.locator('.actress-card [data-action=actress]').first().click();await page.getByRole('button',{name:'Add matched releases',exact:true}).click();const dialog=page.getByRole('dialog',{name:'Add matched releases?',exact:true});
    assert.equal(await dialog.locator('[name=search]').isChecked(),true);assert.equal(await dialog.locator('[name=search]').isDisabled(),true);
    assert.equal(requests.some(r=>r.path.endsWith('/acquire')),false);await dialog.getByRole('button',{name:'Confirm bulk add',exact:true}).click();
    await dialog.locator('.bulk-result').getByText('Monitoring restored. Search started.',{exact:true}).waitFor();await dialog.getByText('1 already present',{exact:true}).waitFor();await dialog.getByText('0 excluded by scope',{exact:true}).waitFor();
    assert.equal(await dialog.locator('.bulk-result img').count(),0);assert.deepEqual(requests.find(r=>r.path.endsWith('/acquire')).body,{scope:'all',search:true});assert.deepEqual(errors,[]);
  }finally{await page.close();}
});

test('watch and cast person birthdays share an inline age badge without mobile overflow',async()=>{
  const person={...actress(),birthday:'1993-08-16',biography:'Unchanged biography'};
  const {page,errors}=await app({withRelease:true,hash:'#/actresses',handle:({path})=>{
    if(path==='/api/actresses/2')return {data:{actress:person,releases:[],total:0}};
    if(path==='/api/releases/1/cast')return {data:{items:[{...person,id:22}],coverage:'Test'}};
    if(path==='/api/people/123456')return {data:{person,releases:[]}};
  }});
  try {
    await page.clock.install({time:new Date('2026-10-18T12:00:00Z')});
    await page.locator('.actress-card [data-action=actress]').first().click();let dialog=page.getByRole('dialog',{name:'Actress watch',exact:true});
    for(const width of [1280,390]){await page.setViewportSize({width,height:850});await dialog.locator('.birthday-fact .badge').getByText('33 years old',{exact:true}).waitFor();assert.equal(await dialog.evaluate(el=>el.scrollWidth<=el.clientWidth),true);}
    await page.keyboard.press('Escape');await page.goto(origin+'#/release/1');await page.locator('[data-action=person]').first().click();dialog=page.getByRole('dialog').last();
    await dialog.locator('.birthday-fact .badge').getByText('33 years old',{exact:true}).waitFor();assert.equal(await dialog.evaluate(el=>el.scrollWidth<=el.clientWidth),true);assert.deepEqual(errors,[]);
  }finally{await page.close();}
});

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
    assert.equal(await confirm.locator('[name=search]').isChecked(),true);
    await confirm.getByRole('button',{name:'Confirm add',exact:true}).click();
    await confirm.getByText('Added and verified in Radarr.',{exact:true}).waitFor();
    assert.deepEqual(requests.find(r=>r.path==='/api/releases/1/acquire').body,{target:'radarr',search:true,quality_profile_id:7,root_folder:'/media/movies'});
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
    assert.deepEqual(requests.find(r=>r.path==='/api/actresses/2/acquire').body,{scope:'all',search:true});
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


test('actress portraits show the full source image without a face-obscuring fade on desktop and mobile',async()=>{
 const svg='<svg xmlns="http://www.w3.org/2000/svg" width="400" height="600"><rect width="400" height="600" fill="#d9e5ee"/><ellipse cx="200" cy="260" rx="125" ry="170" fill="#ba8d71"/><circle cx="150" cy="220" r="10"/><circle cx="250" cy="220" r="10"/><path d="M155 330 Q200 365 245 330" fill="none" stroke="#713e35" stroke-width="10"/><path d="M50 600 Q50 420 200 420 Q350 420 350 600" fill="#314d6c"/></svg>';
 const {page,errors}=await app({hash:'#/actresses',artworkSVG:svg,handle:({path})=>path==='/api/actresses'?{data:{items:[{...actress(),profile:'https://image.tmdb.org/t/p/w342/synthetic-portrait.png'}],total:1}}:undefined});
 try {
  const portrait=page.locator('.actress-portrait');const image=portrait.locator('img');await image.waitFor();

  await page.waitForFunction(()=>document.querySelector('.actress-portrait img')?.naturalHeight===600);
  for(const width of [1440,1920,390]) {
   await page.setViewportSize({width,height:1000});
   const style=await image.evaluate(img=>{const portrait=img.closest('.actress-portrait'),box=portrait.getBoundingClientRect();return {fit:getComputedStyle(img).objectFit,fade:getComputedStyle(portrait,'::after').backgroundImage,width:box.width,height:box.height};});
   assert.equal(style.fit,'contain',`Full portrait must remain visible at ${width}px, not cover-cropped`);
   assert.equal(style.fade,'none',`A gradient must not obscure the face at ${width}px`);
   if(width>540)assert.ok(style.height/style.width>=1.45,'Desktop portrait must have portrait proportions, not a shallow banner');
   else assert.ok(await portrait.evaluate(node=>node.getBoundingClientRect().right<=node.parentElement.querySelector('.actress-body').getBoundingClientRect().left+1),'Mobile portrait must not overlap the name and biography');
   assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),'Portrait must not cause horizontal overflow');
  }
  assert.deepEqual(errors,[]);
 }finally{await page.close();}
});


test('release details label supplied MDBList title links beside provider links without unsafe destinations',async()=>{
  const source='https://mdblist.com/movie/7ry-the-turin-horse';
  const {page,errors}=await app({withRelease:true,hash:'#/release/1',handle:({path})=>path==='/api/releases/1'?{data:{...release(),source_links:[source,'https://www.themoviedb.org/movie/654321','javascript:alert(1)','https://mdblist.com.evil/movie/tt0200374']}}:undefined});
  try {
    const link=page.getByRole('link',{name:'MDBList title',exact:true});await link.waitFor();
    assert.equal(await link.getAttribute('href'),source);assert.equal(await link.getAttribute('rel'),'noopener noreferrer');
    assert.equal(await link.locator('..').getByRole('link',{name:'IMDb title',exact:true}).count(),1);
    assert.equal(await page.locator('a[href^="javascript:"]').count(),0);assert.deepEqual(errors,[]);
  }finally{await page.close();}
});


test('watch and cast details omit ages for missing invalid and future birthdays',async()=>{
  for(const birthday of [undefined,'2026-02-29','2999-01-01']) {
    const person={...actress(),birthday};
    const {page}=await app({withRelease:true,hash:'#/actresses',handle:({path})=>{
      if(path==='/api/actresses/2')return {data:{actress:person,releases:[],total:0}};
      if(path==='/api/releases/1/cast')return {data:{items:[person],coverage:'Test'}};
      if(path==='/api/people/123456')return {data:{person,releases:[]}};
    }});
    try {
      await page.locator('.actress-card [data-action=actress]').first().click();
      await page.locator('.birthday-fact').waitFor();assert.equal(await page.locator('.birthday-fact .badge').count(),0);
      await page.keyboard.press('Escape');await page.goto(origin+'#/release/1');await page.locator('[data-action=person]').first().click();
      await page.locator('.birthday-fact').waitFor();assert.equal(await page.locator('.birthday-fact .badge').count(),0);
    }finally{await page.close();}
  }
});
