# Media Release Watcher — implementation contract

Go net/http app + PostgreSQL, module `media-release-watcher`, Go1.25+. Canonical project directory: `/workspace/media-release-watcher`, a real directory (not a symlink). No sibling repo or Hermes job changes. No real secrets, private rosters or account responses in tracked files.

OWNERS: backend agent owns cmd/, internal/app/ EXCEPT internal/app/web/, go.mod/sum and schema. Frontend owns ONLY internal/app/web/. Integration agent owns ONLY internal/integrations/. Parent owns deployment/docs/verification. Keep boundaries to avoid overwrites.

App entry cmd/media-release-watcher/main.go; Go embeds static internal/app/web/index.html,app.js,styles.css. DATABASE_URL, APP_ADDR default :8080, DATA_DIR. MDBLIST_API_KEY bootstrap from environment. APP_SECRET32+ random chars to encrypt DB secret settings AES-GCM. Single-user first-run setup POST /api/setup (password), login/logout session HttpOnly SameSite cookie, bcrypt, expiry, auth on APIs. Reject cross-origin writes, body size caps. /healthz DB readiness publicly. No secret readback in settings. Blank secret PUT preserves old. Automation and Pushover disabled initially. Notifications never flood historic backfill. Treat connection settings as sensitive.

## APIs (JSON snake_case; errors {error:string}; all lists nonnull arrays)
GET /api/session -> {authenticated,needs_setup}; POST /api/setup,/api/login {password}; POST /api/logout.
GET /api/dashboard -> {stats:{releases,upcoming,current,past,actresses,monitored},featured:[Release],upcoming:[Release],recent:[Release],activity:[Activity],sync:{running,last_run,last_error,stage}}.
GET /api/releases?q=&window=all|past|current|upcoming&type=all|movie|show&group=&actress_id=&sort=release|added|score|title&page=1&limit=36 -> {items:[Release],total,page,limit}. current last30 days inclusive, past older, upcoming future, unknown dates all only. GET /api/releases/{id} -> Release. POST /api/releases {provider:imdb|tmdb,id:string,type:movie|show} fetch real MDBList and save. PATCH {monitored:bool}.
GET /api/actresses?q=&group= -> {items:[Actress],total}; POST {name,tmdb_id:string,group,monitored,auto_add,notify}. Fetch MDBList /people/id, API404 keeps supplied identity with warning (not fabricated profile). POST /api/actresses/import {entries:[{name,actor_id,group}],groups?:object}: preserve source tokens, dedupe by literal ID, queue enrichment. GET /api/actresses/{id} -> {actress:Actress,releases:[Release],total}. PATCH {group,monitored,auto_add,notify}; DELETE local watcher only.
GET /api/search?q=&type=any|movie|show -> {items:[MDBList search rows]}; add chosen release by ID. Actress adding by TMDb ID or MDBList actor URL; don't invent MDBList name-search support.
POST /api/sync ->202 {ok:true}, background,409 if running. GET /api/activity ->{items:[Activity],total}.
GET/PUT /api/settings -> {mdblist:{configured,api_key only input,list_ids:string,sync_interval_minutes},sonarr:{url,configured,api_key only input,quality_profile_id:int,root_folder:string,enabled:bool,search:bool},radarr:{same},pushover:{configured,enabled,device,token/user only input},automation:{enabled:false,notify_first_sync:false},public_url:string}. Optional TMDB_API_KEY fallback allowed only where MDBList lacks credits; must be optional/configured and clearly sourced. Never return keys/user tokens.
POST /api/integrations/sonarr/test or radarr/test ->{ok:true}. GET /api/integrations/{sonarr|radarr}/options ->{root_folders:[{id,path}],quality_profiles:[{id,name}]}.
POST /api/integrations/pushover/test ->{ok:true}; this explicit click sends one test.
POST /api/releases/{id}/acquire {target:radarr|sonarr,search:false,quality_profile_id?:int,root_folder?:string} ->{ok:true,result:{id,already_exists,url}}. Enforce type compatibility and valid root/profile; dedupe & verify external write via exact readback.
POST /api/actresses/{id}/acquire {scope:all|released|upcoming,search:false} ->{results:[{release_id,title,ok,already_exists,error}],added,failed,skipped}. ONLY actual matched releases, each routed movie Radarr/show Sonarr; per-item results, confirmation UI, no silent partial successes.
GET /api/artwork?url= -> cached public HTTPS image proxy allow image.tmdb.org,images.mdblist.com,mdblist.com only, reject internal/other/redirect hosts; timeout,max5MB,image MIME/decode checks.

Release fields: {id:int,title,type:movie|show,year:int,imdb_id,tmdb_id:string,tvdb_id:string,mdblist_id,poster,backdrop,description,country,genres:[string],runtime:int,release_date,digital_date,status,certification,age_rating:int|null,score:number|null,ratings:[object],content_info:object,watch_providers:[object],next_episode:object|null,actresses:[{id,name,group,profile}],monitored:bool,acquisition:{sonarr:string,radarr:string},created_at,updated_at}. Preserve raw MDBList data & source in details. Unknown content explicit; no nudity-scene inference from parental numbers/keywords. Cert and suggested age separate; no region-availability inference from provider arrays.
Actress: {id:int,tmdb_id:string,name,group,profile,biography,birthday,place_of_birth,monitored,auto_add,notify,release_count:int,upcoming_count:int,metadata_status,updated_at}. Detail includes biography+photo+all matched releases and bulk quick-add.
Activity: {id:int,kind,message,status,created_at}.

## Integration package contract
`internal/integrations` package integrations; interface exact for backend interoperability:
```
type Config struct {MDBListAPIKey,MDBListBaseURL,SonarrURL,SonarrAPIKey,RadarrURL,RadarrAPIKey,PushoverToken,PushoverUser,PushoverURL string}
func NewClient(cfg Config) *Client
func(c *Client) MDBGet(ctx context.Context,path string,q url.Values)(json.RawMessage,error)
func(c *Client) MDBBatch(ctx context.Context,provider,kind string,ids []string)([]json.RawMessage,error)
func(c *Client) ArrOptions(ctx context.Context,kind string)(Options,error)
func(c *Client) Add(ctx context.Context,kind string,media Media,options AddOptions)(AddResult,error)
func(c *Client) Notify(ctx context.Context,n Notification)error
type Options struct {RootFolders []RootFolder `json:"root_folders"`;QualityProfiles []QualityProfile `json:"quality_profiles"`}
type RootFolder struct {ID int `json:"id"`;Path string `json:"path"`}
type QualityProfile struct {ID int `json:"id"`;Name string `json:"name"`}
type Media struct {Title,Type,IMDbID,PosterURL string;TMDbID,TVDbID,Year int}
type AddOptions struct {QualityProfileID int;RootFolderPath string;Monitored,Search bool;Tags []int;MinimumAvailability string}
type AddResult struct {ID int `json:"id"`;AlreadyExists bool `json:"already_exists"`;URL string `json:"url"`}
type Notification struct {Title,Message,URL,URLTitle,Device,AttachmentName string;Attachment []byte}
```
Timeouts and sanitized errors (never key/query URL), honor429 no tight retries. Primary MDBList default https://api.mdblist.com GET /user,/people/id,/lists/ID/items (cursor),/people/favorites/upcoming,/catalog/movie|show,/search/movie|show|any,/imdb|tmdb/movie|show/ID; batch POST /tmdb/movie|show IDs supported semantics read-only. Use no remote account writes.
Sonarr/Radarr X-Api-Key; GET /api/v3/rootfolder,/qualityprofile; lookup /series/lookup?term=tvdb:ID (fallback imdb:ID), /movie/lookup?term=tmdb:ID (fallback imdb:ID); lookup metadata supplies seasons/titleSlug. SearchForMissingEpisodes/searchForMovie explicit from request. Existing-check by IDs, POST target then GET exact returned id for verification. Return AlreadyExists idempotently.
Pushover multipart to https://api.pushover.net/1/messages.json token,user,title,message,url,url_title,device,attachment binary<=5MB; ensure status1 evenHTTP200. Adapter tests use httptest mock, never real writes/notifications.

## MDBList-first discovery
Import full configured list pages and favorites-upcoming plus persisted watched-title updates. Real MDBList metadata enrichment with bounded concurrency, cache, transactional UPSERT, partial failures preserve data, progress/activity, periodic scheduler. MDBList /people is BIO ONLY, NOT filmography. No cast data => do NOT invent individual actress/title matches. Upcoming favorites may carry matched-person links. Optionally configured TMDb credits fallback ONLY where MDBList lacks credits; enrich found titles via MDBList. No fallback key => clearly state limitation. No automatic MDBList favorites mutation. Per-actress auto-add/notify + global automation enabled gates; initial baseline silent, durable unique outbox/actions dedupe, failures visible. Persist coverage and source links.

## Visual direction
Cinematic near-black/navy + crimson/coral (F95/JAVBeacon) and DLsite metadata density, gold upcoming, teal success. Left rail, custom MRW logo, striking real-backdrop hero if real data; beautiful poster grids object-fit contain 2:3; tabs past/current/upcoming; calendar/timeline; group filters, search/sort, card size toggle; actresses portrait cards + bio detail, all actual matched releases + individual/bulk add. Activity progress console. Settings integration forms, test states, root/profile selectors. Mobile responsive, toasts/loading/empty/error states, accessible keyboard/dialogs. No fake media/counters in production; honest but beautiful empty/setup states. User rosters runtime import only, not tracked seeds. No external notifications/acquisition until configured.
