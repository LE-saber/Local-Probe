import fs from 'node:fs';
import vm from 'node:vm';
import assert from 'node:assert/strict';

const html = fs.readFileSync(new URL('./index.html', import.meta.url), 'utf8');
const source = html.match(/<script>([\s\S]*?)<\/script>/)[1];
new vm.Script(source);
assert(!/字体规格|已加载|fontInfo|fontHeading|verifyFonts/.test(html));
assert(!/\b(?:fetch\s*\(|XMLHttpRequest|WebSocket|localStorage|sessionStorage|showDirectoryPicker|showOpenFilePicker|FileReader|\.text\s*\()/i.test(source));
const markup = html.split('<script>')[0];
assert(!/class="rail"|id="inspectorBtn"|id="previewBadge"|id="inspector"/.test(markup));
assert(markup.includes('webkitdirectory'));
assert(markup.indexOf('id="folderPicker"') < markup.indexOf('</form></dialog>'));
assert(html.includes('[hidden]{display:none!important}'));
assert(!/side-scope[^>]*style=/.test(markup));
assert(markup.indexOf('id="connectionControls"') < markup.indexOf('id="connections"'));
assert(markup.includes('id="connectionHistory"'));
assert(markup.includes('class="access-layout"'));
assert(markup.includes('id="scopeTree"'));
assert(!/preferenceHeading|connectionSettings/.test(markup));
assert(/id="configImportButton" disabled/.test(markup));
assert(/<html[^>]*data-theme="dark"/.test(html));
const dark = html.match(/\[data-theme="dark"\]\{([^}]+)\}/)[1];
assert(dark.includes('--bg:#181818'));
assert(dark.includes('--accent:#d6cfc2'));
assert(html.includes('font-size:calc(13px * var(--font-scale,1))'));

// Template/event sinks only: no browser, CSS layout, native picker or pixels.
class Sink {
  constructor(id = '') {
    Object.assign(this, { id, innerHTML:'', textContent:'', value:'', dataset:{}, attrs:{}, open:false, hidden:false, disabled:false, checked:false, isConnected:true, listeners:{} });
    const classes = new Set();
    this.classList = {
      add: x => classes.add(x),
      remove: x => classes.delete(x),
      contains: x => classes.has(x),
      toggle: (x, force) => { const on = force ?? !classes.has(x); if(on) classes.add(x); else classes.delete(x); return on; },
    };
  }
  setAttribute(k,v) { this.attrs[k]=v; }
  addEventListener(k,fn) { (this.listeners[k] ||= []).push(fn); }
  dispatch(k, extras={}) { for(const fn of this.listeners[k] || []) fn({target:this,preventDefault(){},...extras}); }
  focus() {}
  click() { this.dispatch('click'); }
  showModal() { this.open=true; }
  close() { this.open=false; this.dispatch('close'); }
  setCustomValidity(v) { this.validityMessage=v; }
  reportValidity() {}
}
const ids=[...markup.matchAll(/\bid="([^"]+)"/g)].map(x=>x[1]);
assert.equal(ids.length,new Set(ids).size);
const nodes=new Map(ids.map(id=>[id,new Sink(id)]));
const dynamicIDs=['themeSelect','languageSelect','fontSizeSelect','activeConnectionSelect'];
dynamicIDs.forEach(id=>nodes.set(id,new Sink(id)));
const pages=['overview','connections','access','activity','settings'].map(id=>nodes.get(id));
const headings=pages.map(p=>Object.assign(new Sink(),{closest:()=>p}));
const dialogs=ids.filter(id=>id.endsWith('Dialog')).map(id=>nodes.get(id));
let appVisible=false,mobile=false;
const document={
  listeners:{},
  documentElement:{dataset:{},lang:'zh-CN',style:{values:{},setProperty(k,v){this.values[k]=v;}}},
  getElementById:id=>{assert(nodes.has(id),'Unknown DOM ID: '+id);return nodes.get(id);},
  querySelector:selector=>selector==='.app'?(appVisible?nodes.get('app'):null):selector==='dialog[open]'?(dialogs.find(d=>d.open)||null):selector==='.page.active .page-title'?headings[0]:null,
  querySelectorAll:selector=>selector==='.page'?pages:selector==='.page-title'?headings:selector==='dialog'?dialogs:[],
  addEventListener(k,fn){(this.listeners[k] ||= []).push(fn);},
};
const window={matchMedia:()=>({matches:mobile}),addEventListener(){}};
const context=vm.createContext({document,window,Date,setTimeout,clearTimeout,requestAnimationFrame:fn=>fn()});
vm.runInContext(source,context);
const run=expr=>vm.runInContext(expr,context), get=id=>nodes.get(id);
appVisible=true;
run('bindUI();renderAll()');
assert.equal(run('state.theme'),'dark');
assert.equal(get('pageNav').innerHTML,get('quickNav').innerHTML);
assert(run('navMarkup()').includes('title="概览"'));
run('toggleSidebar()');
assert.equal(run('state.collapsed'),true);
assert(get('app').classList.contains('collapsed'));
assert.equal(get('sidebarToggle').textContent,'>');
assert.equal(get('sidebarToggle').attrs['aria-expanded'],'false');
run('toggleSidebar()');
assert.equal(get('sidebarToggle').textContent,'<');
mobile=true;
run('toggleSidebar()');
assert(get('sidebar').classList.contains('open'));
assert.equal(get('sidebarToggle').attrs['aria-expanded'],'true');
run('navigate("connections")');
assert(!get('sidebar').classList.contains('open'));
mobile=false;

// Each saved configuration is independent; using history preserves its settings.
const configID=run('addConnection("<New>",{transport:"cloudflare",port:9123,profile:"test-profile"})');
assert(get('connectionHistory').innerHTML.includes(configID));
assert(get('connectionHistory').innerHTML.includes('&lt;New&gt;'));
assert(!get('connectionHistory').innerHTML.includes('<New>'));
assert(run('useConnection('+JSON.stringify(configID)+')'));
assert.equal(run('state.page'),'overview');
assert.equal(run('activeConnection().transport'),'cloudflare');
assert.equal(run('activeConnection().port'),9123);
assert.equal(run('activeConnection().profile'),'test-profile');
assert(get('connectionControls').innerHTML.includes('127.0.0.1:9123'));
assert(get('overviewFlow').innerHTML.includes('未验证'));
run('previewConnection('+JSON.stringify(configID)+',"disconnect")');
assert.equal(run('activeConnection().stage'),'stopped');
assert(get('overviewFlow').innerHTML.includes('已停止'));
assert(run('editConnection('+JSON.stringify(configID)+',"Changed",{transport:"openai",port:9887,profile:"other-profile"})'));
assert(run('useConnection('+JSON.stringify(configID)+')'));
assert.equal(run('activeConnection().port'),9887);
assert.equal(run('state.connections[1].port'),8788);
assert.equal(run('addConnection("Invalid",{port:22})'),false);
assert.equal(run('previewConnection("demo-connection-01","invalid")'),false);

// Tree and right list share the root selection; pause/remove icons stay accessible.
assert.equal((get('scopeTree').innerHTML.match(/data-tree="readonly"/g)||[]).length,3);
for(const group of ['readonly','command','custom']) assert(get('scopeTree').innerHTML.includes('data-group="'+group+'"'));
run('toggleTreeGroup("readonly")');
assert.equal(run('state.treeOpen.readonly'),false);
assert(get('scopeTree').innerHTML.includes('class="tree-children" hidden'));
run('toggleTreeGroup("readonly")');
run('toggleTreeBranch("command:demo-root-01")');
assert.equal(run('state.branchesExpanded["command:demo-root-01"]'),true);
run('toggleTreeBranch("command:demo-root-01")');
assert.equal(run('state.branchesExpanded["command:demo-root-01"]'),false);
run('selectScopeRoot("command","demo-root-02")');
assert(get('rootList').innerHTML.includes('tool_version'));
assert(get('rootList').innerHTML.includes('tool_exists'));
assert(get('rootList').innerHTML.includes('data-action="permissions-root"'));
assert(!get('rootList').innerHTML.includes('data-action="run-command"'));
assert(get('rootList').innerHTML.includes('Research'));
assert(!get('rootList').innerHTML.includes('Local-Probe'));
run('selectTree("readonly","")');
const rootID=run('addRoot("<img src=x onerror=alert(1)>","D:\\\\Demo\\\\Extra")');
assert(get('scopeTree').innerHTML.includes(rootID));
assert(get('scopeTree').innerHTML.includes('&lt;img'));
assert(!get('scopeTree').innerHTML.includes('<img'));
run('selectTree("readonly",'+JSON.stringify(rootID)+')');
assert(get('rootList').innerHTML.includes(rootID));
assert(!get('rootList').innerHTML.includes('demo-root-01'));
assert(get('rootList').innerHTML.includes('aria-label="暂停"'));
run('toggleRoot('+JSON.stringify(rootID)+')');
assert(get('scopeTree').innerHTML.includes('tree-entry paused'));
assert(get('rootList').innerHTML.includes('aria-label="恢复"'));
run('act("scope-all")');
assert(get('rootList').innerHTML.includes('demo-root-01'));

// Browser picker consumes only the directory name. Absolute path stays manual.
run('openEdit("root")');
assert(run('folderPicked([{webkitRelativePath:"Picked/src/main.go"}])'));
assert.equal(get('itemName').value,'Picked');
assert.equal(get('itemPath').value,'');
assert.equal(get('browseNote').hidden,false);
get('editForm').dispatch('submit');
const pickedID=run('state.roots.at(-1).id');
assert.equal(run('state.roots.at(-1).path'),'Picked');
assert.equal(run('state.roots.at(-1).pathResolved'),false);
assert.equal(run('state.roots.at(-1).enabled'),false);
assert(get('rootList').innerHTML.includes('已选目录'));
assert(get('rootList').innerHTML.includes('data-action="resolve-root"'));
assert.equal(run('toggleRoot('+JSON.stringify(pickedID)+')'),false);
run('act("resolve-root",'+JSON.stringify(pickedID)+')');
get('itemPath').value='D:\\Picked';
get('itemPath').dispatch('input');
get('editForm').dispatch('submit');
assert.equal(run('state.roots.find(r=>r.id==='+JSON.stringify(pickedID)+').pathResolved'),true);
assert.equal(run('state.roots.find(r=>r.id==='+JSON.stringify(pickedID)+').enabled'),true);
assert(!run('folderPicked([])'));
assert(run('isAbsolutePath("D:\\\\Work\\\\App")'));
assert(run('isAbsolutePath("\\\\\\\\server\\\\share\\\\folder")'));
assert(!run('isAbsolutePath("relative/path")'));
run('openEdit("root")');
assert.equal(get('rootFields').hidden,false);
assert.equal(get('connectionFields').hidden,true);
get('itemName').value='Form folder';
get('itemPath').value='relative/path';
const beforeInvalid=run('state.roots.length');
get('editForm').dispatch('submit');
assert.equal(run('state.roots.length'),beforeInvalid);
assert(get('itemPath').validityMessage);
get('itemPath').value='D:\\Picked';
get('itemPath').dispatch('input');
get('editForm').dispatch('submit');
assert.equal(run('state.roots.length'),beforeInvalid+1);
assert.equal(get('editDialog').open,false);

// Developer mode and one-time confirmation cannot start any process.
run('openPermissions()');
assert.equal(get('permissionRoot').value,'demo-root-01');
assert(get('permissionRules').innerHTML.includes('tool_version'));
run('closeDialog($("permissionDialog"))');
run('selectScopeRoot("command","demo-root-02");openPermissions()');
assert.equal(get('permissionRoot').value,'demo-root-02');
run('closeDialog($("permissionDialog"));selectTree("readonly","")');
assert(run('savePermissions(true,"demo-root-01",["tool-version"],"src/**/*.go")'));
assert.equal(run('state.commandRules.length'),1);
assert.equal(run('state.customRules.length'),1);
assert(get('scopeTree').innerHTML.includes('tool_version'));
assert(get('scopeTree').innerHTML.includes('src/**/*.go'));
run('selectTree("command","")');
assert(get('rootList').innerHTML.includes('data-action="run-command"'));
const commandID=run('state.commandRules[0].id');
assert.equal(run('confirmCommand('+JSON.stringify(commandID)+')'),false);
assert(run('runCommand('+JSON.stringify(commandID)+')'));
assert(run('confirmCommand('+JSON.stringify(commandID)+')'));
assert.equal(run('confirmCommand('+JSON.stringify(commandID)+')'),false);
assert.equal(run('state.logs[0].result'),'pending');
run('toggleRoot("demo-root-01")');
assert.equal(run('runCommand('+JSON.stringify(commandID)+')'),false);
run('toggleRoot("demo-root-01")');
run('state.developer=false');
assert.equal(run('runCommand('+JSON.stringify(commandID)+')'),false);
run('state.developer=true');

// Filter by space + transport + full timestamp + category + result simultaneously.
const matching=run('filterLogs("","",{workspace:"demo-root-02",transport:"openai",category:"directory"}).length');
assert(matching>0);
assert.equal(run('filterLogs("","",{workspace:"demo-root-02",transport:"cloudflare"}).length'),0);
assert.equal(run('filterLogs("","",{from:"2099-01-01T00:00",until:"2099-02-01T00:00"}).length'),0);
assert.equal(run('filterLogs("","",{from:"2099-02-01T00:00",until:"2000-01-01T00:00"}).length'),0);
get('logWorkspace').value='demo-root-02';
get('logTransport').value='openai';
run('renderLogs()');
assert(get('logTable').innerHTML.includes('Research'));
assert(!get('logTable').innerHTML.includes(rootID));
run('act("reset-filters")');
assert.equal(get('logWorkspace').value,'');
assert(get('logTable').innerHTML.includes('<table'));

// Font size is functional; maintenance cannot falsely claim real checks passed.
assert(run('setFontSize(18)'));
assert.equal(document.documentElement.style.values['--font-scale'],18/14);
assert.equal(run('setFontSize(80)'),false);
run('act("check-health");act("check-update")');
assert(get('healthSettings').innerHTML.includes('未接入'));
assert(get('aboutSettings').innerHTML.includes('无法核对最新版本'));
assert(get('healthSettings').innerHTML.includes('页面配置'));
assert.equal(get('maintenanceConfirm').hidden,true);
assert.equal(run('healthChecks().every(x=>x.ok)'),true);
run('state.connections[0].port=0');
assert.equal(run('healthChecks().find(x=>x.key==="uiConnections").ok'),false);
run('state.connections[0].port=8787');

// In-memory backup restores configuration, but never a connected runtime state.
run('createBackup()');
const original=run('state.roots.length');
run('addRoot("Temporary","D:\\\\Temp")');
assert.equal(run('restoreBackup()'),false);
run('act("restore-backup")');
assert(run('restoreBackup()'));
assert.equal(run('state.roots.length'),original);
assert(run('state.connections.every(c=>c.stage==="stopped")'));
assert.equal(run('new Set(state.roots.map(r=>r.id)).size'),run('state.roots.length'));
run('addRoot("After restore","D:\\\\Temp")');
assert.equal(run('new Set(state.roots.map(r=>r.id)).size'),run('state.roots.length'));

for(const page of pages) {run('navigate('+JSON.stringify(page.id)+')');assert.equal(run('state.page'),page.id);}
run('toggleTheme();setLocale("en")');
assert.equal(run('state.theme'),'light');
assert(get('pageNav').innerHTML.includes('Access scope'));
assert(get('overviewFlow').innerHTML.includes('Unverified'));
assert(get('connectionHistory').innerHTML.includes('Use'));
run('removeItem("root",'+JSON.stringify(rootID)+')');
assert(!get('scopeTree').innerHTML.includes(rootID));
run('state.connections=[];renderAll()');
assert(get('overviewFlow').innerHTML.includes('No connections'));
assert(!get('overviewFlow').innerHTML.includes('class="flow"'));
run('removeItem("root","demo-root-02")');
assert.equal(run('state.logs[0].workspaceName'),'Research');
assert(run('filterLogs("","",{workspace:"demo-root-02"}).length')>0);
console.log('PASS: 13-comment UI structure, sidebar state, saved configuration reuse, tree/list linkage, picker metadata, combined filters, fixed command confirmation, maintenance boundaries, memory restore, escaping and IDs.');
console.log('NOT TESTED: real browser rendering, pixels, native directory picker, OS or service integration.');
