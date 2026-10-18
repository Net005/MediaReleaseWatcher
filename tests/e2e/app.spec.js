import {test,expect} from '@playwright/test';

// Explicit synthetic API fixtures. These tests never contact media managers,
// external metadata accounts or Pushover, and do not seed production data.
const today = new Date().toISOString().slice(0,10);
const future = new Date(Date.now()+10*86400000).toISOString().slice(0,10);
const actress = {id:1,tmdb_id:'123456789',name:'Synthetic Performer',group:'Test group',profile:'',biography:'Synthetic biography for browser contract testing.',birthday:'1990-01-01',place_of_birth:'Test fixture',monitored:true,auto_add:false,notify:false,release_count:2,upcoming_count:1,metadata_status:'Synthetic test coverage'};
const releases = [
  {id:1,title:'Synthetic Current Film',type:'movie',year:new Date().getFullYear(),release_date:today},
  {id:2,title:'Synthetic Future Series',type:'show',year:new Date().getFullYear(),release_date:future},
  {id:3,title:'Synthetic Past Film',type:'movie',year:2000,release_date:'2000-01-01'}
].map(r=>({...r,imdb_id:'tt1234567',tmdb_id:String(r.id),tvdb_id:'1234',mdblist_id:'',poster:'',backdrop:'',description:'Synthetic description, not catalogue data.',country:'Test fixture',genres:['Drama'],runtime:90,digital_date:'',status:'',certification:'Not verified',age_rating:null,score:null,ratings:[],content_info:{status:'unverified'},watch_providers:[],next_episode:null,actresses:[actress],monitored:true,acquisition:{sonarr:'',radarr:''}}));

async function fixtures(page,{authenticated=true,needsSetup=false,withArtwork=false}={}) {
  const calls=[];const errors=[];let watchlisted=false;
  const watched=[actress,{...actress,id:2,tmdb_id:'123456788',name:'Synthetic No Upcoming',upcoming_count:0,release_count:1}];
  page.on('pageerror',error=>errors.push(error.message));
  const settings={mdblist:{configured:true,list_ids:'1,2',sync_interval_minutes:360},sonarr:{url:'http://sonarr.example',configured:true,quality_profile_id:1,root_folder:'/tv',enabled:false,search:false},radarr:{url:'http://radarr.example',configured:true,quality_profile_id:1,root_folder:'/movies',enabled:false,search:false},pushover:{configured:false,enabled:false,device:''},automation:{enabled:false,notify_first_sync:false},public_url:''};
  await page.route('**/api/**',async route=>{
    const req=route.request(),u=new URL(req.url()),path=u.pathname,method=req.method();
    calls.push({path,method,query:Object.fromEntries(u.searchParams),body:req.postDataJSON?.()});
    let data;
    if(path==='/api/artwork')return route.fulfill({status:200,contentType:'image/png',body:Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jhO0AAAAASUVORK5CYII=','base64')});
    if(path==='/api/session')data={authenticated,needs_setup:needsSetup};
    else if(path==='/api/setup'||path==='/api/login'){authenticated=true;needsSetup=false;data={ok:true};}
    else if(path==='/api/logout'){authenticated=false;data={ok:true};}
    else if(path==='/api/dashboard')data={stats:{releases:3,upcoming:1,current:1,past:1,actresses:1,monitored:3},featured:[releases[0]],upcoming:[releases[1]],recent:releases,activity:[],sync:{running:false,last_run:null,last_error:'',stage:'idle'}};
    else if(path==='/api/actresses/1/acquire')data={results:[{release_id:1,title:releases[0].title,ok:true,already_exists:false,error:''},{release_id:2,title:releases[1].title,ok:true,already_exists:true,error:''}],added:1,failed:0,skipped:1};
    else if(path==='/api/actresses/1')data={actress,releases:releases.slice(0,2),total:2};
    else if(path==='/api/actresses/import'){
      const payload=req.postDataJSON();let imported=0,duplicates=0;
      for(const entry of payload.entries){
        if(watched.some(a=>a.tmdb_id===entry.actor_id)){duplicates++;continue;}
        watched.push({...actress,id:watched.length+1,tmdb_id:entry.actor_id,name:entry.name,
          group:entry.group??payload.group??'',monitored:entry.monitored??payload.monitored??true,
          auto_add:entry.auto_add??payload.auto_add??false,notify:entry.notify??payload.notify??false});imported++;
      }
      data={imported,duplicates,errors:[]};
    }
    else if(path==='/api/actresses'){
      let items=watched;
      if(u.searchParams.get('upcoming_only')==='true')items=items.filter(a=>a.upcoming_count>0);
      if(u.searchParams.get('group'))items=items.filter(a=>a.group===u.searchParams.get('group'));
      data={items,total:items.length};
    }
    else if(path==='/api/mdblist/lists')data={items:[{id:'1',name:'Synthetic personal list',enabled:true,monitored:false,auto_add:false}]};
    else if(path==='/api/tasks')data={items:[]};
    else if(path==='/api/silo/status')data={configured:false,status:'unknown'};
    else if(path==='/api/arr/sync'||path==='/api/silo/sync')data={ok:true};
    else if(/^\/api\/releases\/\d+\/cast$/.test(path))data={items:[{tmdb_id:actress.tmdb_id,name:actress.name,character:'Synthetic role',profile:''}],coverage:'Synthetic cast fixture'};
    else if(/^\/api\/people\/\d+$/.test(path))data={person:actress,releases:releases.slice(0,2)};
    else if(/^\/api\/releases\/\d+\/watchlist$/.test(path)){if(method==='POST')watchlisted=req.postDataJSON().in_watchlist;data={status:watchlisted?'present':'absent',in_watchlist:watchlisted,verified:method==='POST',checked_at:new Date().toISOString()};}
    else if(path==='/api/releases'){
      let items=releases;const w=u.searchParams.get('window'),kind=u.searchParams.get('type'),q=(u.searchParams.get('q')||'').toLowerCase();
      if(w==='upcoming')items=items.filter(r=>r.id===2);if(w==='current')items=items.filter(r=>r.id===1);if(w==='past')items=items.filter(r=>r.id===3);
      if(kind&&kind!=='all')items=items.filter(r=>r.type===kind);if(q)items=items.filter(r=>r.title.toLowerCase().includes(q));
      data={items,total:items.length,page:1,limit:36};
    }
    else if(/^\/api\/releases\/\d+$/.test(path))data=releases.find(r=>String(r.id)===path.split('/').at(-1));
    else if(path.endsWith('/acquire'))data={ok:true,result:{id:12,already_exists:false,url:'http://radarr.example/movie/test'}};
    else if(path==='/api/settings'){if(method==='PUT')Object.assign(settings,req.postDataJSON());data=settings;}
    else if(path.endsWith('/options'))data={root_folders:[{id:1,path:path.includes('sonarr')?'/tv':'/movies'}],quality_profiles:[{id:1,name:'Synthetic HD profile'}]};
    else if(path.endsWith('/test')||path==='/api/sync')data={ok:true};
    else if(path==='/api/activity')data={items:[],total:0};
    else if(path==='/api/search')data={items:[]};
    else return route.fulfill({status:404,contentType:'application/json',body:JSON.stringify({error:'Unmapped synthetic test route'})});
    if(withArtwork)data=JSON.parse(JSON.stringify(data), (key,value)=>key==='poster'?'https://image.tmdb.org/t/p/w342/synthetic-fixture.png':value);
    await route.fulfill({status:200,contentType:'application/json',body:JSON.stringify(data)});
  });
  return {calls,errors};
}

test('initializes the real module and navigates populated catalogue views',async({page})=>{
 const {errors}=await fixtures(page);await page.goto('/');
 await expect(page.locator('#app')).toBeVisible();await expect(page.locator('#boot')).toBeHidden();
 await expect(page.getByText('Active actress watches',{exact:true})).toBeVisible();
 await page.locator('[data-nav="releases"]').click();await expect(page.locator('#main')).toContainText('Synthetic Current Film');
 await expect(page.locator('#main')).toContainText('Synthetic Future Series');
 await page.locator('[data-nav="calendar"]').click();await expect(page.locator('#calendar-content')).toBeVisible();
 await page.locator('[data-nav="activity"]').click();await expect(page.locator('#main')).toBeVisible();
 expect(errors).toEqual([]);
});

test('actress card opens biography and all actually matched releases',async({page})=>{
 const {errors}=await fixtures(page);await page.goto('/#/actresses');
 await page.locator('.actress-card [data-action="actress"]').first().click();
 const dialog=page.locator('dialog[open]').last();await expect(dialog).toContainText('Synthetic biography');
 await expect(dialog).toContainText('Synthetic Current Film');await expect(dialog).toContainText('Synthetic Future Series');
 await expect(dialog.locator('[data-local="bulk"]')).toBeEnabled();
 await dialog.locator('[data-local="bulk"]').click();await expect(page.locator('dialog[open]')).toHaveCount(2);
 await page.keyboard.press('Escape');await expect(page.locator('dialog[open]')).toHaveCount(1);
 expect(errors).toEqual([]);
});

test('settings opens without returning or pre-filling integration keys',async({page})=>{
 const {errors}=await fixtures(page);await page.goto('/#/settings');
 await expect(page.locator('#main')).toContainText('MDBList');await expect(page.locator('#main')).toContainText('Pushover');
 const inputs=page.locator('#main input[type="password"]');expect(await inputs.count()).toBeGreaterThan(0);
 for(const input of await inputs.all())await expect(input).toHaveValue('');
 expect(errors).toEqual([]);
});

test('first run exposes password setup and completes authenticated navigation',async({page})=>{
 const {errors}=await fixtures(page,{authenticated:false,needsSetup:true});await page.goto('/');
 await expect(page.locator('#auth')).toBeVisible();await expect(page.locator('#auth-confirm-label')).toBeVisible();
 await page.locator('#auth-password').fill('synthetic-browser-password');await page.locator('#auth-confirm').fill('synthetic-browser-password');
 await page.locator('#auth-submit').click();await expect(page.locator('#app')).toBeVisible();expect(errors).toEqual([]);
});

test('available artwork does not show an unavailable label over the poster',async({page})=>{
 await fixtures(page,{withArtwork:true});await page.goto('/#/releases');
 const art=page.locator('.release-card .art').first();
 await expect(art.locator('img')).toBeVisible();
 await expect(art.locator('.art-label')).toBeHidden();
});

test('actress upcoming-only filter changes the API query and restores all watches',async({page})=>{
 const {calls,errors}=await fixtures(page);await page.goto('/#/actresses');
 await expect(page.locator('.actress-card')).toHaveCount(2);
 const upcoming=page.getByRole('checkbox',{name:/upcoming releases only/i});
 await upcoming.check();await expect(page.locator('.actress-card')).toHaveCount(1);
 expect(calls.some(c=>c.path==='/api/actresses'&&c.query.upcoming_only==='true')).toBe(true);
 await upcoming.uncheck();await expect(page.locator('.actress-card')).toHaveCount(2);
 expect(errors).toEqual([]);
});

test('bulk import exposes single-add settings and preserves explicit source entry groups',async({page})=>{
 const {calls,errors}=await fixtures(page);await page.goto('/#/actresses');
 await page.locator('[data-action="import-actresses"]').click();
 const dialog=page.locator('dialog[open]').last();
 await dialog.locator('input[name="group"]').fill('Imported group');
 await dialog.locator('input[name="monitored"]').uncheck();
 await dialog.locator('input[name="auto_add"]').check();
 await dialog.locator('input[name="notify"]').check();
 await dialog.locator('textarea[name="entries"]').fill(JSON.stringify([
  {name:'Synthetic Imported One',actor_id:'987654321'},
  {name:'Synthetic Imported Two',actor_id:'987654320',group:'Preserved group'}
 ]));
 await dialog.locator('button[type="submit"]').click();await expect(dialog).not.toBeVisible();
 const imported=calls.find(c=>c.path==='/api/actresses/import'&&c.method==='POST');
 expect(imported?.body).toMatchObject({group:'Imported group',monitored:false,auto_add:true,notify:true});
 expect(imported.body.entries[1].group).toBe('Preserved group');
 expect(errors).toEqual([]);
});

test('mobile catalogue does not overflow the viewport',async({page})=>{
 await page.setViewportSize({width:390,height:844});const {errors}=await fixtures(page);await page.goto('/#/releases');
 await expect(page.locator('#app')).toBeVisible();await expect(page.locator('#main')).toContainText('Synthetic Current Film');
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth+1)).toBe(true);
 expect(errors).toEqual([]);
});
