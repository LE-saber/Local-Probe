import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {spawnSync} from 'node:child_process';
import vm from 'node:vm';
import {fileURLToPath} from 'node:url';
const asset=new URL('../internal/desktopweb/assets/',import.meta.url);
const [html,css,js,icons]=await Promise.all(['index.html','app.css','app.js','icons.js'].map(f=>readFile(new URL(f,asset),'utf8')));
for(const directive of ["default-src 'none'","script-src 'self'","style-src 'self'","connect-src 'none'","form-action 'none'"])assert.ok(html.includes(directive));
assert.doesNotMatch(html,/<style\b|\sstyle\s*=|<form\b|<iframe\b|(?:src|href)\s*=\s*["']https?:/i);
assert.doesNotMatch(html,/inspectorBtn|previewBadge|fontHeading|class="rail"/);
assert.match(html,/src="\.\/icons\.js"/);
assert.match(css,/--bg:#181818/);
assert.match(css,/@media\s*\(max-width:760px\)/);
assert.match(css,/prefers-reduced-motion/);
assert.doesNotMatch(css,/user-select\s*:\s*none/);
assert.match(css,/button,input,select,textarea\{color:inherit/,'multiline editors must inherit dark/light text color');
assert.match(css,/\[data-theme="dark"\]\{color-scheme:dark/);
assert.match(css,/\[data-theme="light"\]\{color-scheme:light/);
assert.doesNotMatch(js,/\bfetch\s*\(|XMLHttpRequest|WebSocket|window\.prompt|seedFolders|simulate-error/);
assert.doesNotMatch(js,/addEventListener\(["'](?:contextmenu|copy|selectstart|beforeunload|unload)["']/);
for(const method of ['snapshot','connections.save','connections.select','connections.remove','connection.connect','connection.reconnect','connection.disconnect','connection.cancel','workspace.pick','workspace.add','workspace.update','workspace.remove','settings.backup','settings.pickBackup','settings.restore','logs.export','diagnostics.export'])assert.ok(js.includes('"'+method+'"'),method);
for(const file of ['icons.js','app.js']){const check=spawnSync(process.execPath,['--check',fileURLToPath(new URL(file,asset))],{encoding:'utf8'});assert.equal(check.status,0,check.stderr);}
const flush=async()=>{for(let i=0;i<20;i++)await Promise.resolve();};
class Clock{
 now=0;seq=0;timers=new Map();
 timeout=(fn,delay=0)=>this.add(fn,delay,0);interval=(fn,delay)=>this.add(fn,delay,delay);clear=id=>this.timers.delete(id);
 add(fn,delay,repeat){const id=++this.seq;this.timers.set(id,{fn,at:this.now+delay,repeat});return id;}
 async advance(ms){const end=this.now+ms;while(true){const next=[...this.timers].filter(([,v])=>v.at<=end).sort((a,b)=>a[1].at-b[1].at)[0];if(!next)break;const[id,v]=next;this.now=v.at;if(v.repeat)v.at+=v.repeat;else this.timers.delete(id);v.fn();await flush();}this.now=end;await flush();}
}
class Element{
 constructor(doc,tag='div',id=''){this.doc=doc;this.tagName=tag.toUpperCase();this.id=id;this.dataset={};this.listeners=new Map();this.attrs={};this.value='';this.textContent='';this.disabled=false;this.open=false;this.writes=0;this.classes=new Set();this.classList={toggle:(n,on)=>{const add=on??!this.classes.has(n);add?this.classes.add(n):this.classes.delete(n);return add;},add:n=>this.classes.add(n),remove:n=>this.classes.delete(n),contains:n=>this.classes.has(n)};}
 set innerHTML(value){this._html=String(value);this.writes++;this.doc.parse(this._html);if(this.tagName==='SELECT'){const opts=[...this._html.matchAll(/<option\b([^>]*)>/g)];const choice=opts.find(m=>/\bselected\b/.test(m[1]))||opts[0];this.value=choice?.[1].match(/value="([^"]*)"/)?.[1]||'';}}
 get innerHTML(){return this._html||'';}
 setAttribute(k,v){this.attrs[k]=String(v);if(k.startsWith('data-'))this.dataset[k.slice(5).replace(/-([a-z])/g,(_,x)=>x.toUpperCase())]=String(v);}
 addEventListener(k,fn){const list=this.listeners.get(k)||[];list.push(fn);this.listeners.set(k,list);}
 dispatch(k,e={}){for(const fn of this.listeners.get(k)||[])fn({target:this,...e});}
 closest(s){return s==='button'&&this.tagName==='BUTTON'?this:null;}
 focus(){this.doc.activeElement=this;}
 showModal(){this.open=true;this.focus();}
 close(){this.open=false;this.dispatch('close');}
 reportValidity(){this.validityReported=true;return false;}
 prepend(e){this.doc.elements.set(e.id,e);}
 querySelector(s){if(s==='h1'){if(!this.heading)this.heading=new Element(this.doc,'h1');return this.heading;}return null;}
}
class Document{
 constructor(){this.elements=new Map();this.listeners=new Map();this.hidden=false;this.documentElement=new Element(this,'html');this.body=new Element(this,'body');this.activeElement=this.body;this.parse(html);}
 parse(markup){for(const m of markup.matchAll(/<([\w-]+)\b([^>]*\bid="([^"]+)"[^>]*)>/g)){const e=new Element(this,m[1],m[3]);for(const a of m[2].matchAll(/([\w-]+)="([^"]*)"/g))e.setAttribute(a[1],a[2]);e.value=e.attrs.value||'';e.disabled=/\bdisabled\b/.test(m[2]);this.elements.set(e.id,e);}}
 getElementById=id=>this.elements.get(id)||null;
 createElement=tag=>new Element(this,tag);
 querySelector(s){return this.querySelectorAll(s)[0]||null;}
 querySelectorAll(s){return [...this.elements.values()].filter(e=>e.tagName==='DIALOG'&&(s==='dialog'||s==='dialog[open]'&&e.open));}
 addEventListener(k,fn){const list=this.listeners.get(k)||[];list.push(fn);this.listeners.set(k,list);}
 dispatch(k,e){for(const fn of this.listeners.get(k)||[])fn(e);}
}
function harness({bridge=true,prefs='{}',themeOK=true,themeAutoReply=true}={}){
 const clock=new Clock(),document=new Document(),requests=[],themes=[],storage=new Map([['localProbeGUI',prefs]]);let listener;
 const localStorage={getItem:k=>storage.get(k)||null,setItem:(k,v)=>storage.set(k,v)},selection={isCollapsed:true};
 const window={localStorage,getSelection:()=>selection,chrome:bridge?{webview:{addEventListener:(_type,fn)=>listener=fn,postMessage:raw=>{const request=JSON.parse(raw);if(request.method==='window.theme'){themes.push(request);if(themeAutoReply)listener({data:JSON.stringify({id:request.id,ok:themeOK,data:themeOK?{theme:request.params.theme}:undefined,error:themeOK?undefined:{code:'theme_unavailable'}})});}else requests.push(request);}}}:undefined};
 vm.runInNewContext(icons+'\n'+js,{window,document,localStorage,setTimeout:clock.timeout,clearTimeout:clock.clear,setInterval:clock.interval,clearInterval:clock.clear,console},{timeout:1500});
 return{clock,document,requests,themes,window,storage,selection,nativeReady(){listener({data:JSON.stringify({event:'window.ready'})});},el:id=>document.getElementById(id),reply(request,data,ok=true,id=request.id,code='operation_failed'){listener({data:JSON.stringify({id,ok,data,error:ok?undefined:{code}})});},click(action,id='',group=''){const e=new Element(document,'button');e.dataset={action,id,group};document.dispatch('click',{target:e});},clickID(id){const e=document.getElementById(id);assert.ok(e,id);if(!e.disabled){e.dispatch('click');document.dispatch('click',{target:e});}},page(page){const e=new Element(document,'button');e.dataset.page=page;document.dispatch('click',{target:e});},change(id,value){const e=document.getElementById(id);e.value=value;document.dispatch('change',{target:e});}};
}
function snapshot(){return{connection:{transport:'openai_runtime',stage:'idle',status_known:true,mcp_ready:false,tunnel_ready:false,busy:false,generation:1},workspace:{available:true,revision:'r1',connection_id:'c1',profile_id:'p1',roots:[{id:'root1',path:'D:\\work',display_name:'work',enabled:true}]},desktop:{version:'R10-desktop',revision:'r1',active_id:'c1',connections:[{id:'c1',name:'My connection',profile_id:'p1',transport:'openai_runtime',port:8787,enabled:true}],events_available:true,events:[],root_rules:[]},view:{logs:[]}};}
async function ready(app,data=snapshot()){app.nativeReady();app.reply(app.requests[0],data);await flush();return data;}
{
 const a=harness({bridge:false});await flush();assert.match(a.el('hostNotice').textContent,/桌面宿主未连接/);assert.equal(a.requests.length,0);assert.equal(a.el('addConnectionButton').disabled,true);
}
{
 const a=harness();assert.equal(a.requests[0].method,'snapshot');a.reply(a.requests[0],snapshot(),true,'wrong');await flush();assert.match(a.el('hostNotice').textContent,/读取/);await ready(a);assert.equal(a.el('hostNotice').hidden,true);
 assert.match(a.el('overviewFlow').innerHTML,/本机 MCP/);assert.match(a.el('overviewFlow').innerHTML,/ChatGPT/);assert.match(a.el('pageNav').innerHTML,/<svg[^]*nav-label/);
 assert.match(a.el('connectionHistory').innerHTML,/class="data-table history-table"/);assert.equal((a.el('connectionHistory').innerHTML.match(/<th scope="col">/g)||[]).length,5);assert.equal((a.el('connectionHistory').innerHTML.match(/<td>/g)||[]).length,4);assert.match(a.el('connectionHistory').innerHTML,/>操作<\/th>/);assert.match(css,/\.history-table\{table-layout:fixed;min-width:860px;width:100%\}/);
 assert.doesNotMatch(a.el('aboutSettings').innerHTML,/data-action="diagnostics"/);assert.match(a.el('healthSettings').innerHTML,/data-action="diagnostics"/);assert.match(a.el('backupSettings').innerHTML,/无需填写后缀/);
 a.clickID('sidebarToggle');assert.equal(a.el('app').classList.contains('collapsed'),true);assert.equal(a.el('sidebarToggle').textContent,'>');a.clickID('sidebarToggle');assert.equal(a.el('sidebarToggle').textContent,'<');
 a.clickID('themeBtn');assert.equal(a.document.documentElement.dataset.theme,'light');a.page('settings');a.change('settingSize','18');assert.equal(a.document.documentElement.dataset.size,'18');
 const reload=harness({prefs:a.storage.get('localProbeGUI')});assert.equal(reload.document.documentElement.dataset.size,'18');assert.equal(reload.requests.length,1);assert.equal(reload.requests[0].method,'snapshot','refresh must not restart the host connection');
}
{
 const a=harness(),data=snapshot();data.connection.status_known=false;data.connection.mcp_ready=true;data.connection.tunnel_ready=true;await ready(a,data);assert.doesNotMatch(a.el('overviewFlow').innerHTML,/>就绪</);
 const before=a.el('overviewFlow').writes;await a.clock.advance(4000);a.reply(a.requests.at(-1),data);await flush();assert.equal(a.el('overviewFlow').writes,before,'unchanged poll must not remount the page');
 a.selection.isCollapsed=false;await a.clock.advance(4000);data.connection.status_known=true;a.reply(a.requests.at(-1),data);await flush();assert.equal(a.el('overviewFlow').writes,before,'poll must preserve selected text');
 a.document.hidden=true;const count=a.requests.length;await a.clock.advance(8000);assert.equal(a.requests.length,count,'hidden window must not poll');
}
{
 const a=harness(),data=snapshot();data.workspace.roots[0].display_name='<img src=x onerror=alert(1)>';data.workspace.roots[0].path='D:\\<svg onload=alert(2)>';await ready(a,data);assert.match(a.el('rootList').innerHTML,/&lt;img/);assert.doesNotMatch(a.el('rootList').innerHTML,/<img src=x/);assert.match(a.el('rootList').innerHTML,/&lt;svg/);
 a.click('add-root');assert.equal(a.el('editDialog').open,true);a.el('itemPath').value='D:\\keep';a.el('itemPath').focus();await a.clock.advance(4000);a.reply(a.requests.at(-1),snapshot());await flush();assert.equal(a.el('itemPath').value,'D:\\keep');assert.equal(a.el('editDialog').open,true);
 a.el('itemName').value='';a.click('browse');assert.equal(a.requests.at(-1).method,'workspace.pick');a.reply(a.requests.at(-1),{paths:['D:\\chosen']});await flush();assert.equal(a.el('itemPath').value,'D:\\chosen');assert.equal(a.el('itemName').value,'chosen');a.clickID('editSubmit');assert.equal(a.requests.at(-1).method,'workspace.add');assert.equal(a.requests.at(-1).params.expected_revision,'r1','picker response must not erase the current snapshot');assert.equal(a.requests.at(-1).params.display_name,'chosen');a.reply(a.requests.at(-1),snapshot());await flush();assert.equal(a.el('editDialog').open,false);
}
{
 const a=harness();await ready(a);a.click('add-connection');a.el('itemName').value='Second';a.el('itemPort').value='8891';a.clickID('editSubmit');const request=a.requests.at(-1);assert.equal(request.method,'connections.save');assert.equal(request.params.port,8891);assert.equal(request.params.transport,'openai_runtime');assert.equal(request.params.id,'');assert.equal(request.params.expected_revision,'r1');a.reply(request,snapshot());await flush();
 a.click('select-connection','c1');assert.equal(a.el('confirmDialog').open,true);assert.match(a.el('confirmCopy').textContent,/不会自动连接/);a.clickID('confirmRemove');assert.equal(a.requests.at(-1).method,'connections.select');a.reply(a.requests.at(-1),snapshot());await flush();
 a.click('backup');assert.equal(a.requests.at(-1).method,'settings.backup');assert.equal(a.el('confirmDialog').open,false);a.reply(a.requests.at(-1),snapshot());await flush();
 a.click('restore');assert.equal(a.requests.at(-1).method,'settings.pickBackup');assert.equal(a.el('confirmDialog').open,false);await a.clock.advance(20000);const picked=snapshot();picked.backup_selection={selection_id:'selected-backup',expected_revision:'r1',created_at:'2026-10-06T01:00:00Z',connections:2,profiles:2,roots:3};a.reply(a.requests.at(-1),picked);await flush();assert.match(a.el('confirmCopy').textContent,/所有目录暂停/);assert.match(a.el('confirmCopy').textContent,/连接 2 个/);assert.equal(a.el('confirmDialog').open,true);a.clickID('confirmRemove');assert.equal(a.requests.at(-1).method,'settings.restore');assert.equal(a.requests.at(-1).params.selection_id,'selected-backup');assert.equal(a.requests.at(-1).params.expected_revision,'r1');a.reply(a.requests.at(-1),snapshot());await flush();
}
{
 const a=harness(),data=snapshot();data.desktop.events=[{timestamp:'2026-10-05T10:00:00Z',event_type:'workspace.update',connection_id:'c1',root_ids:['root1'],transport:'openai_runtime',outcome:'success'},{timestamp:'2026-10-05T11:00:00Z',event_type:'connection.connect',connection_id:'c2',root_ids:[],transport:'cloudflare_named',outcome:'failure'}];data.view.logs=[{timestamp:'2026-10-05T12:00:00Z',event_type:'file.read',connection_id:'c1',component:'files',outcome:'success'}];await ready(a,data);assert.equal(a.el('eventCount').textContent,'3');a.change('logWorkspace','root1');assert.equal(a.el('eventCount').textContent,'1');assert.match(a.el('logTable').innerHTML,/workspace.update/);a.click('reset-filters');a.change('logTransport','cloudflare_named');assert.equal(a.el('eventCount').textContent,'1');a.click('reset-filters');a.change('logTransport','unknown');assert.equal(a.el('eventCount').textContent,'1','runtime audit must not inherit the current transport');a.click('check-update');assert.match(a.el('maintenanceBody').innerHTML,/未联网/);a.click('close-dialog');a.click('scope-group','command');assert.match(a.el('rootList').innerHTML,/保持关闭/);assert.doesNotMatch(a.requests.map(x=>x.method).join(),/command.run/);
}
{
 const a=harness();await ready(a);await a.clock.advance(4000);const old=a.requests.at(-1);a.click('connect');const write=a.requests.at(-1);assert.equal(write.method,'connection.connect');const changed=snapshot();changed.desktop.revision='r2';changed.workspace.revision='r2';a.reply(write,changed);await flush();a.reply(old,snapshot());await flush();a.click('backup');assert.equal(a.requests.at(-1).params.expected_revision,'r2','late poll must not roll back a mutation snapshot');
}
{
 const a=harness();await ready(a);await a.clock.advance(4000);const old=a.requests.at(-1);await a.clock.advance(15000);assert.match(a.el('hostNotice').textContent,/超时/);const count=a.requests.length;a.click('connect');assert.equal(a.requests.length,count,'stale snapshots must prevent mutations');a.reply(old,snapshot());await flush();assert.match(a.el('hostNotice').textContent,/超时/);await a.clock.advance(1000);a.reply(a.requests.at(-1),snapshot());await flush();assert.equal(a.el('hostNotice').hidden,true);
}
{
 const a=harness(),data=snapshot();data.connection.busy=true;await ready(a,data);a.click('cancel');assert.equal(a.requests.at(-1).method,'connection.cancel');a.reply(a.requests.at(-1),snapshot(),false,undefined,'stop_failed');await flush();assert.match(a.el('toast').textContent,/安全停止/);
}
{
 const a=harness();await ready(a);a.click('restore');a.reply(a.requests.at(-1),snapshot());await flush();assert.equal(a.el('confirmDialog').open,false,'cancelled picker must not open restore confirmation');a.click('restore');a.reply(a.requests.at(-1),snapshot(),false,undefined,'backup_invalid');await flush();assert.equal(a.el('confirmDialog').open,false);assert.match(a.el('toast').textContent,/当前配置未更改/);assert.equal(a.requests.filter(r=>r.method==='settings.restore').length,0);
}
{
 const a=harness(),data=snapshot();data.desktop.developer={enabled:false,allowed_connections:[],execution_available:false,gate_code:'network_enforcement_required',profiles:[]};await ready(a,data);a.click('permissions');assert.equal(a.el('developerMode').disabled,false);assert.equal(a.el('permissionSave').hidden,false);a.el('developerMode').checked=true;a.clickID('permissionSave');assert.match(a.el('toast').textContent,/至少一个连接/);assert.equal(a.el('permissionDialog').open,true);a.el('developerConnection0').checked=true;
 await a.clock.advance(4000);const changed=snapshot();changed.desktop.revision='r2';a.reply(a.requests.at(-1),changed);await flush();a.clickID('permissionSave');assert.equal(a.el('confirmDialog').open,true);assert.match(a.el('confirmCopy').textContent,/安全门/);a.clickID('confirmRemove');const request=a.requests.at(-1);assert.equal(request.method,'developer.configure');assert.equal(request.params.expected_revision,'r1','bind permission confirmation to the revision viewed, not the latest poll');assert.deepEqual(request.params.allowed_connections,['c1']);assert.equal(request.params.enabled,true);a.reply(request,changed);await flush();
}
{
 const a=harness(),data=snapshot();data.desktop.developer={enabled:true,allowed_connections:['c1'],execution_available:false,profiles:[]};await ready(a,data);a.click('permissions');assert.equal(a.el('developerMode').checked,true);assert.equal(a.el('developerConnection0').checked,true);a.el('developerMode').checked=false;a.clickID('permissionSave');a.clickID('confirmRemove');assert.deepEqual(a.requests.at(-1).params.allowed_connections,[]);assert.equal(a.requests.at(-1).params.enabled,false);a.reply(a.requests.at(-1),snapshot());await flush();a.click('scope-group','command');assert.match(a.el('rootList').innerHTML,/新增模板/);assert.match(a.el('rootList').innerHTML,/执行保持关闭/);
 a.click('add-command');assert.equal(a.el('commandDialog').open,true);a.el('commandJSON').value='broken';a.clickID('commandSave');assert.equal(a.el('commandError').hidden,false);assert.equal(a.el('commandDialog').open,true);
 const p={id:'version',kind:'version_probe',executable:'C:\\Tools\\tool.exe',argv:{variants:[{variant_id:'version',exact:['--version']}]}};a.el('commandJSON').value=JSON.stringify(p);a.clickID('commandSave');assert.equal(a.el('confirmDialog').open,true);assert.match(a.el('confirmCopy').textContent,/不会运行/);a.clickID('confirmRemove');const request=a.requests.at(-1);assert.equal(request.method,'developer.saveProfile');assert.equal(request.params.id,'version');assert.deepEqual(request.params.profile,p);a.reply(request,snapshot());await flush();assert.doesNotMatch(a.requests.map(r=>r.method).join(),/command.run|developer.run|run_probe/);
}
{
 const a=harness(),data=snapshot();data.desktop.developer={enabled:false,allowed_connections:[],execution_available:false,profiles:[{id:'tool_version',kind:'version_probe',executable:'<img src=x onerror=evil>',argv:{variants:[]},limits:{wall_timeout_ms:2000}}]};await ready(a,data);a.click('scope-group','command');assert.doesNotMatch(a.el('rootList').innerHTML,/<img/);assert.match(a.el('rootList').innerHTML,/&lt;img/);a.click('edit-command','tool_version');const profile=JSON.parse(a.el('commandJSON').value);profile.id='renamed';a.el('commandJSON').value=JSON.stringify(profile);a.clickID('commandSave');assert.equal(a.el('commandError').hidden,false);a.click('close-dialog');a.click('remove-command','tool_version');a.clickID('confirmRemove');assert.equal(a.requests.at(-1).method,'developer.removeProfile');assert.equal(a.requests.at(-1).params.id,'tool_version');
}
function rulesResponse(data=snapshot()){
 data.file_rules={connection_id:'c1',profile_id:'p1',revision:'r1',profile:{deny_patterns:['global/**'],ignore_patterns:[]},roots:[{root_id:'root1',enabled:true,deny_patterns:['secrets/**'],ignore_patterns:['build/**']}]};return data;
}
function rulePreview(data=snapshot(),changed=true){
 data.rule_preview={connection_id:'c1',revision:'r1',changed,affected_connections:changed?['c1','c2']:[],decisions:[{root_id:'root1',path:'secrets/key.txt',directory:false,allowed:false,ignored:false},{root_id:'root1',path:'build/out.txt',allowed:true,ignored:true}]};return data;
}
async function openRules(a,root='root1'){
 a.click('edit-rules',root);assert.equal(a.requests.at(-1).method,'rules.read');assert.equal(a.requests.at(-1).params.connection_id,'c1');a.reply(a.requests.at(-1),rulesResponse());await flush();assert.equal(a.el('rulesDialog').open,true);
}
{
 const a=harness();await ready(a);a.click('scope-group','custom');assert.match(a.el('rootList').innerHTML,/连接规则/);assert.match(a.el('rootList').innerHTML,/编辑目录规则/);await openRules(a);
 assert.equal(a.el('ruleDeny').value,'secrets/**','editor must load root layer, not merged rules');assert.equal(a.el('ruleIgnore').value,'build/**');assert.equal(a.el('rulesApplyButton').disabled,true);
 a.el('ruleSamples').value='secrets/key.txt\nbuild/out.txt\nsrc/';a.clickID('rulesPreviewButton');const p=a.requests.at(-1);assert.equal(p.method,'rules.preview');assert.equal(p.params.expected_revision,'r1');assert.equal(p.params.update.root_id,'root1');assert.equal(p.params.samples[2].path,'src');assert.equal(p.params.samples[2].directory,true);
 a.reply(p,rulePreview());await flush();assert.match(a.el('rulesPreviewResult').innerHTML,/拒绝/);assert.match(a.el('rulesPreviewResult').innerHTML,/c1 · c2/);assert.equal(a.el('rulesApplyButton').disabled,true);
 a.el('rulesOfflineAck').checked=true;a.el('rulesOfflineAck').dispatch('change');assert.equal(a.el('rulesApplyButton').disabled,false);a.clickID('rulesApplyButton');assert.match(a.el('confirmCopy').textContent,/不写工作文件/);assert.match(a.el('confirmCopy').textContent,/c1 · c2/);a.clickID('confirmRemove');const save=a.requests.at(-1);assert.equal(save.method,'rules.apply');assert.equal(save.params.connection_id,'c1');assert.equal(save.params.offline_confirmed,true);assert.deepEqual(save.params.acknowledged_connections,['c1','c2']);assert.equal(save.params.samples,undefined);a.reply(save,snapshot());await flush();
}
{
 const a=harness();await ready(a);await openRules(a,'');assert.equal(a.el('ruleDeny').value,'global/**');assert.equal(a.el('ruleIgnore').value,'');a.clickID('rulesPreviewButton');assert.equal(a.requests.at(-1).params.update.root_id,'');a.reply(a.requests.at(-1),rulePreview(snapshot(),false));await flush();a.el('rulesOfflineAck').checked=true;a.el('rulesOfflineAck').dispatch('change');assert.equal(a.el('rulesApplyButton').disabled,true,'unchanged policy must not be saved');
}
{
 const a=harness();await ready(a);await openRules(a);a.clickID('rulesPreviewButton');const preview=a.requests.at(-1);a.el('ruleDeny').value='changed/**';a.el('ruleDeny').dispatch('input');a.reply(preview,rulePreview());await flush();assert.equal(a.el('rulesPreviewResult').innerHTML,'','late preview must not validate edited input');assert.equal(a.el('rulesApplyButton').disabled,true);
 a.clickID('rulesPreviewButton');a.reply(a.requests.at(-1),rulePreview());await flush();a.el('rulesOfflineAck').checked=true;a.el('rulesOfflineAck').dispatch('change');a.el('ruleSamples').value='other.txt';a.el('ruleSamples').dispatch('input');assert.equal(a.el('rulesOfflineAck').checked,false);assert.equal(a.el('rulesApplyButton').disabled,true);
}
{
 const a=harness();await ready(a);await openRules(a);a.el('ruleDeny').value='draft/**';a.clickID('rulesPreviewButton');a.reply(a.requests.at(-1),rulePreview());await flush();a.el('rulesOfflineAck').checked=true;a.el('rulesOfflineAck').dispatch('change');
 await a.clock.advance(4000);const newer=snapshot();newer.desktop.revision='r2';newer.workspace.revision='r2';a.reply(a.requests.at(-1),newer);await flush();assert.equal(a.el('ruleDeny').value,'draft/**');
 a.clickID('rulesApplyButton');a.clickID('confirmRemove');assert.equal(a.requests.at(-1).params.expected_revision,'r1','save must bind to the preview, not a newer poll');a.reply(a.requests.at(-1),newer,false,undefined,'revision_conflict');await flush();a.click('close-dialog');
 a.click('edit-rules','root1');const reopened=rulesResponse(newer);reopened.file_rules.revision='r2';a.reply(a.requests.at(-1),reopened);await flush();assert.equal(a.el('ruleDeny').value,'draft/**','failed save must retain draft for a new preview');assert.equal(a.el('rulesApplyButton').disabled,true);
}
{
 const a=harness();await ready(a);await openRules(a);a.clickID('rulesPreviewButton');const malicious=rulePreview();malicious.rule_preview.decisions[0].path='<img src=x onerror=evil>';malicious.rule_preview.affected_connections=['<svg onload=evil>'];a.reply(a.requests.at(-1),malicious);await flush();assert.doesNotMatch(a.el('rulesPreviewResult').innerHTML,/<img|<svg/);assert.match(a.el('rulesPreviewResult').innerHTML,/&lt;img/);a.el('rulesOfflineAck').checked=true;a.el('rulesOfflineAck').dispatch('change');a.el('ruleDeny').value='unpreviewed/**';a.clickID('rulesApplyButton');assert.equal(a.el('confirmDialog').open,false,'even a programmatic edit must invalidate the saved preview');
}
{
 const a=harness();await ready(a);await openRules(a);a.el('ruleDeny').value='../invalid';a.clickID('rulesPreviewButton');a.reply(a.requests.at(-1),snapshot(),false,undefined,'invalid_rules');await flush();assert.equal(a.el('rulesError').hidden,false);assert.match(a.el('rulesError').textContent,/相对 glob/);assert.equal(a.el('ruleDeny').value,'../invalid');assert.equal(a.el('rulesApplyButton').disabled,true);assert.equal(a.requests.filter(r=>r.method==='rules.apply').length,0);
}
for(const method of ['rules.read','rules.preview','rules.apply'])assert.ok(js.includes('"'+method+'"'));
assert.doesNotMatch(css,/(?:zoom\s*:|filter\s*:\s*blur|transform\s*:\s*scale\()/i,'never solve DPI by scaling a rendered bitmap');
{
 const a=harness({prefs:JSON.stringify({theme:'light'})});assert.equal(a.themes.length,0,'wait for trusted native ready event');await ready(a);assert.equal(a.themes.length,1);assert.equal(a.themes[0].params.theme,'light');a.page('settings');assert.equal(a.themes.length,1,'navigation must not resend cosmetic changes');a.clickID('themeBtn');a.clickID('themeBtn');await flush();assert.deepEqual(a.themes.map(r=>r.params.theme),['light','dark','light']);a.change('settingTheme','dark');await flush();assert.equal(a.themes.at(-1).params.theme,'dark');await a.clock.advance(4000);a.reply(a.requests.at(-1),snapshot());await flush();assert.equal(a.themes.length,4,'polls must not keep repainting the native frame');assert.equal(a.requests.filter(r=>r.method.startsWith('connection.')).length,0);const reload=harness({prefs:a.storage.get('localProbeGUI')});await ready(reload);assert.equal(reload.themes[0].params.theme,'dark','reload restores the native theme');
}
{
 const a=harness({themeOK:false});await ready(a);await a.clock.advance(1600);assert.match(a.el('toast').textContent,/原生主题未能同步/);assert.equal(a.el('hostNotice').hidden,true,'cosmetic failure must not break backend UI');a.page('settings');assert.equal(a.themes.length,4,'three retries, then no render spam');assert.equal(a.requests.filter(r=>r.method.startsWith('connection.')).length,0);
}
{
 const a=harness({prefs:JSON.stringify({theme:'light'})});a.nativeReady();a.reply(a.requests[0],snapshot(),false,undefined,'config_invalid');await flush();assert.equal(a.themes[0].params.theme,'light','native theme must work before/without backend health');a.clickID('themeBtn');await flush();assert.equal(a.themes.at(-1).params.theme,'dark');assert.match(a.el('hostNotice').textContent,/配置无效/);
}
{
 const a=harness({themeAutoReply:false});await ready(a);a.reply(a.themes[0],{theme:'dark'});await flush();a.clickID('themeBtn');const light=a.themes.at(-1);a.clickID('themeBtn');assert.equal(a.themes.length,2,'serialize native transitions');a.reply(light,undefined,false,undefined,'theme_unavailable');await flush();assert.equal(a.themes.length,3,'late failed light transition must reapply latest dark');assert.equal(a.themes.at(-1).params.theme,'dark');a.reply(a.themes.at(-1),{theme:'dark'});await flush();a.page('settings');assert.equal(a.themes.length,3,'confirmed final theme must not be resent');
}
{
 const a=harness({themeAutoReply:false});await ready(a);a.reply(a.themes[0],{},true);await flush();await a.clock.advance(250);assert.equal(a.themes.length,2,'missing applied-theme acknowledgement must retry');a.reply(a.themes.at(-1),{theme:'dark'});await flush();a.clickID('themeBtn');const failed=a.themes.at(-1);a.reply(failed,undefined,false,undefined,'theme_unavailable');await flush();await a.clock.advance(250);assert.equal(a.themes.at(-1).params.theme,'light');a.reply(a.themes.at(-1),{theme:'light'});await flush();const count=a.themes.length;a.nativeReady();a.page('settings');assert.equal(a.themes.length,count,'a successful retry must settle without loops');
}
console.log('PASS approved desktop layout and 24 VM scenarios including native readiness, acknowledged/retried/coalesced themes, layered rules, stale previews, draft retention, XSS escaping and gated execution');
