/* Run: node --test internal/web/testdata/schedule_interaction.cjs */
const test = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname, '../static/schedule.js'), 'utf8');

class Node {
  constructor(data = {}) { this.dataset = data; this.handlers = {}; this.attributes = {}; this.hidden = false; this.disabled = false; this.textContent = ''; this.classList = { toggle() {} }; }
  addEventListener(name, callback) { (this.handlers[name] ||= []).push(callback); }
  async emit(name, fields = {}) { const event = { target:this, preventDefault() { this.prevented = true; }, ...fields }; for (const callback of this.handlers[name] || []) await callback(event); return event; }
  setAttribute(name,value) { this.attributes[name] = value; }
  removeAttribute(name) { delete this.attributes[name]; }
  getBoundingClientRect() { return { top:100, height:40 }; }
  querySelector(selector) { return this.one?.[selector] || null; }
  querySelectorAll(selector) { return this.many?.[selector] || []; }
  closest(selector) { return selector === '[data-day]' ? this.day || null : selector === '[data-slot]' ? this : null; }
}
function setup(storage = new Map()) {
  const root = new Node(), doc = new Node(), win = new Node();
  const editor = new Node(), form = new Node(), save = new Node(), status = new Node(), error = new Node(), scroll = new Node(), native = new Node();
  scroll.scrollTop=0;scroll.scrollHeight=5000;scroll.clientHeight=480;
  win.sessionStorage={getItem:key=>storage.get(key)||null,setItem:(key,value)=>storage.set(key,value),removeItem:key=>storage.delete(key)};
  const zone = new Node(), detected = new Node(); zone.value = 'UTC'; form.action = 'http://example.test/c/1/schedule/slots';
  const days = ['2030-01-01','2030-01-02'].map(date => new Node({day:date}));
  const slots = days.flatMap(day => ['15:00','15:30','16:00','16:30'].map(clock => {
    const slot = new Node({slot:`${day.dataset.day}T${clock}:00Z`,state:'unknown',original:'unknown'});
    slot.day=day;slot.title=`${day.dataset.day} ${clock} +00:00: unknown`;
    slot.one={'.slot-time':Object.assign(new Node(),{textContent:clock}),'.slot-symbol':new Node()};return slot;
  }));
  days.forEach(day => { day.many={'[data-slot]':slots.filter(slot => slot.day===day)};day.one={'.day-heading':new Node()}; });
  const modes = ['want','cant','unknown'].map(mode=>new Node({mode}));
  const tabs = days.map(day=>new Node({dayTab:day.dataset.day}));
  const clears=days.map(day=>Object.assign(new Node({clearDay:day.dataset.day}),{day}));
  root.one={'[data-paint-editor]':editor,'[data-slot-save]':form,'[data-save-button]':save,'[data-save-status]':status,'[data-save-error]':error,'[data-week-scroll]':scroll,'[data-timezone]':zone,'[data-detected-zone]':detected,'[data-native-editor]':native};
  root.many={'[data-slot]':slots,'[data-mode]':modes,'[data-day-tab]':tabs,'[data-clear-day]':clears,'[data-day]':days};
  doc.querySelector=()=>root;
  let hit=slots[0], reloads=0, requests=[], failure=true;
  doc.elementFromPoint=()=>hit;
  const context={document:doc,window:win,navigator:{},location:{href:'http://example.test',reload(){reloads++;}},Intl,URL,URLSearchParams,FormData:class { *[Symbol.iterator](){yield ['csrf','token'];yield ['tz','UTC'];} },
    fetch:async(url,request)=>{requests.push(request);return {ok:!failure,redirected:false,json:async()=>({saved:true})};}};
  win.confirm=()=>false;
  vm.runInNewContext(source,context);
  return {root,doc,win,slots,modes,tabs,clears,form,save,status,error,editor,native,scroll,storage,requests,get reloads(){return reloads},set hit(value){hit=value},set failure(value){failure=value}};
}
const mouse=(extra={})=>({button:0,pointerId:1,pointerType:'mouse',clientX:10,clientY:10,...extra});

test('desktop drag fills crossed cells once; erase, keyboard, day tabs and leave guard',async()=>{
 const f=setup();assert.equal(f.editor.hidden,false);assert.equal(f.native.hidden,true);
 await f.slots[0].emit('pointerdown',mouse());f.hit=f.slots[3];await f.doc.emit('pointermove',mouse({clientY:140}));await f.doc.emit('pointerup',mouse());
 assert.deepEqual(f.slots.slice(0,4).map(s=>s.dataset.state),['want','want','want','want']);
 assert.match(f.status.textContent,/4 changed slots/);
 await f.modes[2].emit('click');await f.slots[1].emit('click',{detail:0});assert.equal(f.slots[1].dataset.state,'unknown');assert.match(f.status.textContent,/3 changed slots/);
 await f.tabs[1].emit('click');assert.match(f.status.textContent,/3 changed slots/);
 const leave=await f.win.emit('beforeunload');assert.equal(leave.prevented,true);
 const settings=await f.root.emit('submit',{target:new Node()});assert.equal(settings.prevented,true);
 await f.clears[0].emit('click');assert.equal(f.save.disabled,true);assert.match(f.status.textContent,/Saved/);
});

test('touch scrolling does not paint; tap and keyboard paint without double toggles',async()=>{
 const f=setup();const touch=mouse({pointerType:'touch'});
 await f.slots[0].emit('pointerdown',touch);await f.doc.emit('pointerup',{...touch,clientY:80});assert.equal(f.slots[0].dataset.state,'unknown');
 await f.slots[0].emit('pointerdown',touch);await f.doc.emit('pointercancel',touch);await f.doc.emit('pointerup',touch);assert.equal(f.slots[0].dataset.state,'unknown');
 await f.slots[0].emit('pointerdown',touch);await f.doc.emit('pointerup',touch);await f.slots[0].emit('click',{detail:1});assert.equal(f.slots[0].dataset.state,'want');
 await f.modes[1].emit('click');await f.slots[4].emit('click',{detail:0});assert.equal(f.slots[4].dataset.state,'cant');
});

test('explicit save posts only changed cells; failed request retains paint for retry',async()=>{
 const f=setup();await f.slots[0].emit('click',{detail:0});await f.slots[4].emit('click',{detail:0});
 assert.equal(f.requests.length,0);
 await f.form.emit('submit');assert.equal(f.requests.length,1);assert.equal(f.error.hidden,false);assert.equal(f.save.disabled,false);assert.equal(f.reloads,0);
 const payload=JSON.parse(f.requests[0].body.get('edits'));assert.equal(payload.length,2);assert.equal(payload[0].changes.length,1);assert.equal(payload[1].changes.length,1);
 assert.equal(f.slots[0].dataset.state,'want');assert.match(f.status.textContent,/Unsaved/);
 f.failure=false;await f.form.emit('submit');assert.equal(f.reloads,1);assert.equal(f.save.disabled,true);assert.match(f.status.textContent,/Saved/);
});


test('only confirmed saves restore a one-shot day and scroll bookmark after reload',async()=>{
 const storage=new Map(), f=setup(storage);
 await f.tabs[1].emit('click');f.scroll.scrollTop=1432;
 await f.slots[4].emit('click',{detail:0});await f.form.emit('submit');
 assert.equal(storage.size,0,'failed saves must not bookmark');
 f.failure=false;await f.form.emit('submit');assert.equal(storage.size,1);
 const restored=setup(storage);
 assert.equal(restored.tabs[1].attributes['aria-pressed'],'true');assert.equal(restored.scroll.scrollTop,1432);
 assert.equal(storage.size,0,'bookmark must be consumed');
 const ordinary=setup(storage);assert.notEqual(ordinary.scroll.scrollTop,1432);
});

test('invalid, stale and out-of-week bookmarks are consumed without restoring; storage errors do not break save',async()=>{
 const key='playpile:timetable:/c/1/schedule/slots:UTC';
 for (const saved of ['not json',JSON.stringify({day:'2030-01-09',top:1432,at:Date.now()}),JSON.stringify({day:'2030-01-02',top:1432,at:Date.now()-120000}),JSON.stringify({day:'2030-01-02',top:-4,at:Date.now()})]) {
  const storage=new Map([[key,saved]]),f=setup(storage);assert.equal(storage.size,0);assert.notEqual(f.scroll.scrollTop,1432);
 }
 const f=setup();f.win.sessionStorage={setItem(){throw new Error('disabled')}};
 await f.slots[0].emit('click',{detail:0});f.failure=false;await f.form.emit('submit');assert.equal(f.reloads,1);assert.match(f.status.textContent,/Saved/);
});
