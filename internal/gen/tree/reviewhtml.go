package tree

import (
	"encoding/json"
	"fmt"
	stdhtml "html"
	"os"
	"regexp"
	"strings"
)

// HTMLPath is the page next to the Markdown review: tree-review_gen.html.
func HTMLPath(mdPath string) string {
	return strings.TrimSuffix(mdPath, ".md") + ".html"
}

var dataRE = regexp.MustCompile(`(?s)<script id="review-data" type="application/json">(.*?)</script>`)

// loadTimelines reads the timelines a page of an earlier run holds.
func (d *ReviewDoc) loadTimelines(page string) {
	b, err := os.ReadFile(page)
	if err != nil {
		return
	}
	m := dataRE.FindSubmatch(b)
	if m == nil {
		return
	}
	var data reviewData
	if json.Unmarshal(m[1], &data) != nil {
		return
	}
	for step, list := range data.Timelines {
		d.timelines[step] = list
	}
	d.trace = data.Trace
}

// RenderHTML writes the page of the review: every step with its timeline,
// one row per request or step in the order it ran; a row opens its bodies
// on a click (rendered then, so a page with thousands of requests stays
// fast). Filter and search narrow the rows.
func (d *ReviewDoc) RenderHTML() string {
	var b strings.Builder
	b.WriteString(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Review: tree</title>
<style>` + reviewCSS + `</style></head><body>
<header><h1>Review: tree</h1>`)
	if d.Spec != "" {
		fmt.Fprintf(&b, "<p class=\"muted\">Spec %s · %s · the steps, the next command and the plan: tree-review_gen.md</p>", esc(d.Spec), esc(d.File))
	}
	b.WriteString(`<div class="bar"><input id="q" type="search" placeholder="Search step or path" aria-label="Search step or path">
<label><input id="bad" type="checkbox"> only failed</label><button id="all" type="button">open all steps</button><button id="none" type="button">close all</button></div></header><main>`)
	for i, s := range reviewSteps {
		sec := d.sections[s]
		if sec == nil {
			continue
		}
		state := "done"
		switch {
		case sec.outdated != "":
			state = "outdated"
		case sec.failed:
			state = "failed"
		}
		list := d.timelines[s]
		// closed on every load: the summary line says enough to choose
		fmt.Fprintf(&b, "\n<details class=\"step\" id=\"%s\"><summary><b>%d. %s</b> <span class=\"state %s\">%s</span> %s</summary>",
			s, i+1, esc(reviewTitles[s]), state, state, esc(sec.summary))
		if sec.outdated != "" {
			fmt.Fprintf(&b, "<p class=\"note\">Outdated: %s</p>", esc(sec.outdated))
		}
		if s == ReviewFetch && d.trace != nil {
			b.WriteString("<h3>As a tree</h3><p class=\"muted\">Per node and record above it: the values sent and where they come from, " +
				"the conditions, pick, every request, and per candidate why it was taken or not, with what hangs below it.</p>" +
				"<div class=\"trace\"></div><h3>Requests in the order sent</h3>")
		}
		if list == nil {
			b.WriteString("<p class=\"muted\">No requests or steps of this step.</p>")
		} else {
			fmt.Fprintf(&b, "<div class=\"tl\" data-step=\"%s\"></div>", s)
		}
		b.WriteString("</details>")
	}
	b.WriteString("\n</main>\n<script id=\"review-data\" type=\"application/json\">")
	b.WriteString(d.encodeData())
	b.WriteString("</script>\n<script>" + reviewJS + "</script>\n</body></html>\n")
	return b.String()
}

func esc(s string) string { return stdhtml.EscapeString(s) }

const reviewCSS = `
:root{--bg:#fff;--fg:#1d1f23;--muted:#62666d;--line:#e3e5e8;--soft:#f5f6f8;--ok:#1a7f37;--bad:#c62828;--warn:#9a6700;--code:#f6f8fa}
@media (prefers-color-scheme:dark){:root{--bg:#16181c;--fg:#e6e7e9;--muted:#9aa0a6;--line:#2c2f35;--soft:#1e2126;--ok:#4cc26a;--bad:#ff6b6b;--warn:#e3b341;--code:#1b1e23}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--fg);font:14px/1.45 system-ui,sans-serif}
header{position:sticky;top:0;background:var(--bg);border-bottom:1px solid var(--line);padding:12px 16px;z-index:1}
h1{font-size:18px;margin:0 0 4px}.muted{color:var(--muted);margin:4px 0}.note{color:var(--warn);margin:8px 0}
.bar{display:flex;gap:12px;align-items:center;flex-wrap:wrap;margin-top:8px}
input[type=search]{flex:1;min-width:180px;padding:6px 8px;border:1px solid var(--line);border-radius:6px;background:var(--soft);color:var(--fg)}
button{padding:5px 10px;border:1px solid var(--line);border-radius:6px;background:var(--soft);color:var(--fg);cursor:pointer}
main{padding:8px 16px 40px;max-width:1400px}
details.step{border:1px solid var(--line);border-radius:8px;margin:12px 0;padding:0 12px}
details.step>summary{cursor:pointer;padding:10px 0}
.state{font-size:12px;padding:1px 6px;border-radius:10px;border:1px solid var(--line)}
.state.failed{color:var(--bad)}.state.outdated{color:var(--warn)}.state.done{color:var(--ok)}
table{border-collapse:collapse;width:100%;margin:4px 0 12px}
th,td{text-align:left;padding:4px 6px;border-bottom:1px solid var(--line);vertical-align:top}
th{color:var(--muted);font-weight:600;font-size:12px}
tr.row{cursor:pointer}tr.row:hover{background:var(--soft)}tr.row.bad td.st{color:var(--bad);font-weight:600}tr.row td.st{color:var(--ok)}
td.n,td.ms{color:var(--muted);font-variant-numeric:tabular-nums;white-space:nowrap}td.req{word-break:break-all}
td.res{color:var(--muted);max-width:520px}
tr.body td{background:var(--soft);padding:8px}
.pane{display:grid;grid-template-columns:repeat(auto-fit,minmax(320px,1fr));gap:8px}
.pane h4{margin:0 0 4px;font-size:12px;color:var(--muted)}
pre{margin:0;padding:8px;background:var(--code);border:1px solid var(--line);border-radius:6px;overflow:auto;max-height:480px;font:12px/1.4 ui-monospace,monospace}
.json{font:12px/1.5 ui-monospace,monospace;background:var(--code);border:1px solid var(--line);border-radius:6px;padding:6px 8px;overflow:auto;max-height:600px}
.json details{margin-left:14px}.json details>summary{cursor:pointer;list-style:none;margin-left:-14px}
.json details>summary::before{content:'▸ ';color:var(--muted)}.json details[open]>summary::before{content:'▾ '}
.json .k{color:#8250df}.json .s{color:#0a3069}.json .num{color:#0550ae}.json .lit{color:#cf222e}.json .c{color:var(--muted)}
@media (prefers-color-scheme:dark){.json .k{color:#d2a8ff}.json .s{color:#a5d6ff}.json .num{color:#79c0ff}.json .lit{color:#ff7b72}}
.json .leaf{margin-left:14px;white-space:pre-wrap;word-break:break-all}.json button{margin:2px 0 2px 14px;font-size:12px;padding:1px 8px}
@media (max-width:700px){td.res,th.res,td.ms,th.ms{display:none}}
h3{font-size:14px;margin:12px 0 4px}
.trace details{margin:2px 0 2px 16px;border-left:1px solid var(--line);padding-left:8px}
.trace>details{margin-left:0}
.trace summary{cursor:pointer;padding:2px 0;word-break:break-word}
.vbody{margin:4px 0 6px}
.cond{margin:2px 0}.lbl{color:var(--muted);font-size:12px;margin-right:4px}
.out.ok{color:var(--ok)}.out.bad,.badt{color:var(--bad)}.out.warn,.cond.warn{color:var(--warn)}.out.muted,.why{color:var(--muted)}
.cond.bad{color:var(--bad)}.key{font-family:ui-monospace,monospace}
table.vals{width:auto;min-width:50%}table.vals td{font-family:ui-monospace,monospace;font-size:12px;word-break:break-all}
.reqs,.cands{margin:6px 0}details.req.bad>summary{color:var(--bad)}
`

const reviewJS = `
(function(){
const data=JSON.parse(document.getElementById('review-data').textContent);
const CHUNK=100;
// a JSON value as a tree: every object and list folds; its children are
// built when it is opened, long lists 100 at a time
function el(tag,cls,t){const e=document.createElement(tag);if(cls)e.className=cls;if(t!==undefined)e.textContent=t;return e}
function scalar(v){
  if(typeof v==='string')return el('span','s',JSON.stringify(v));
  if(typeof v==='number')return el('span','num',String(v));
  return el('span','lit',String(v))}
function node(key,v,open){
  const label=el('span');if(key!==null){label.appendChild(el('span','k',JSON.stringify(key)));label.appendChild(document.createTextNode(': '))}
  if(v===null||typeof v!=='object'){const d=el('div','leaf');d.appendChild(label);d.appendChild(scalar(v));return d}
  const arr=Array.isArray(v),keys=arr?null:Object.keys(v),n=arr?v.length:keys.length;
  if(n===0){const d=el('div','leaf');d.appendChild(label);d.appendChild(el('span','c',arr?'[]':'{}'));return d}
  const det=el('details'),sum=el('summary');sum.appendChild(label);
  sum.appendChild(el('span','c',arr?'[ '+n+' items ]':'{ '+n+' keys }'));det.appendChild(sum);
  let built=0;
  function more(){
    const end=Math.min(n,built+CHUNK);
    for(let i=built;i<end;i++)det.insertBefore(arr?node(i,v[i],false):node(keys[i],v[keys[i]],false),det.lastMoreButton||null);
    built=end;
    if(det.lastMoreButton){det.lastMoreButton.remove();det.lastMoreButton=null}
    if(built<n){const b=el('button',null,'show '+Math.min(CHUNK,n-built)+' more of '+(n-built));b.type='button';
      b.onclick=e=>{e.stopPropagation();more()};det.appendChild(b);det.lastMoreButton=b}}
  det.addEventListener('toggle',()=>{if(det.open&&built===0)more()});
  if(open){det.open=true;more()}
  return det}
function pane(title,v){
  const box=el('div');box.appendChild(el('h4',null,title));
  const j=el('div','json');
  if(typeof v==='string'){const d=el('div','leaf',v);j.appendChild(d)}else j.appendChild(node(null,v,true));
  box.appendChild(j);return box}
function cell(tr,cls,t){const td=document.createElement('td');td.className=cls;td.textContent=t;tr.appendChild(td);return td}
function status(e){if(!e.status)return e.expect?'expect '+e.expect:'';let s=e.status+(e.failed?' ✗':' ✓');if(e.expect&&e.expect!==e.status)s+=' (expected '+e.expect+')';return s}
document.querySelectorAll('.tl').forEach(div=>{
  const list=data.timelines[div.dataset.step]||[];
  const t=document.createElement('table');
  t.innerHTML='<thead><tr><th>#</th><th>Step</th><th>Request</th><th>Status</th><th class="ms">ms</th><th class="res">Result</th></tr></thead>';
  const tb=document.createElement('tbody');t.appendChild(tb);
  list.forEach((e,i)=>{
    const tr=document.createElement('tr');tr.className='row'+(e.failed?' bad':'');
    tr.dataset.text=((e.step||'')+' '+(e.method||'')+' '+(e.target||'')+' '+(e.result||'')).toLowerCase();
    cell(tr,'n',i+1);cell(tr,'',e.step||'');cell(tr,'req',((e.method||'')+' '+(e.target||'')).trim());
    cell(tr,'st',status(e));cell(tr,'ms',e.ms||'');cell(tr,'res',e.result||'');
    let open=null;
    tr.onclick=()=>{
      if(open){open.remove();open=null;return}
      open=document.createElement('tr');open.className='body';const td=document.createElement('td');td.colSpan=6;
      const p=document.createElement('div');p.className='pane';
      if(e.hasRequest)p.appendChild(pane('request',e.request));
      if(e.answer!==undefined&&e.answer!==null)p.appendChild(pane('answer',e.answer));
      if(e.result)p.appendChild(pane('result',e.result));
      if(!p.children.length){const m=document.createElement('p');m.className='muted';m.textContent='no body';p.appendChild(m)}
      td.appendChild(p);open.appendChild(td);tr.after(open)};
    tb.appendChild(tr)});
  div.appendChild(t)});
// the fetch as a tree: a visit per node and record above it, its parts
// built when it is opened
const log=data.timelines.fetch||[];
function line(parent,cls,label,t){const d=el('div',cls);if(label){d.appendChild(el('span','lbl',label))}if(t!==undefined)d.appendChild(document.createTextNode(t));parent.appendChild(d);return d}
function fold(title,cls,build,open){const d=el('details',cls),s=el('summary');
  if(typeof title==='string')s.textContent=title;else s.appendChild(title);d.appendChild(s);
  let done=false;const fill=()=>{if(!done){done=true;build(d)}};
  d.addEventListener('toggle',()=>{if(d.open)fill()});if(open){d.open=true;fill()}return d}
function valuesTable(parent,vals){if(!vals||!vals.length)return;
  const t=el('table','vals');t.innerHTML='<thead><tr><th>value</th><th>in</th><th>tree.yaml</th><th>sent</th></tr></thead>';
  const tb=el('tbody');vals.forEach(v=>{const tr=el('tr');
    [v.name,v.in,v.from===undefined?'':(typeof v.from==='string'?v.from:JSON.stringify(v.from)),
     v.error?('✗ '+v.error):(v.value===undefined?'':(typeof v.value==='string'?v.value:JSON.stringify(v.value)))]
     .forEach((x,i)=>{const td=el('td',i===3&&v.error?'badt':'',x);tr.appendChild(td)});tb.appendChild(tr)});
  t.appendChild(tb);parent.appendChild(t)}
function requests(parent,list){if(!list||!list.length)return;
  const box=line(parent,'reqs','requests ('+list.length+')');
  list.forEach(r=>{
    let st=r.error?('✗ '+r.error):(r.status+(r.status>=200&&r.status<300?' ✓':' ✗'));if(r.cached)st+=' · from the cache';
    const head=el('span',null,(r.method+' '+r.target+'  → '+st));
    const bad=r.error||!(r.status>=200&&r.status<300);
    const d=fold(head,'req'+(bad?' bad':''),d=>{
      const e=r.log?log[r.log-1]:null;const p=el('div','pane');
      if(r.for)p.appendChild(pane('for',r.for));
      if(e&&e.hasRequest)p.appendChild(pane('request',e.request));
      if(e&&e.answer!==undefined&&e.answer!==null)p.appendChild(pane('answer',e.answer));
      if(!e)p.appendChild(pane('answer',r.cached?'the same request was sent before: its answer is in the line of that request':'not sent'));
      d.appendChild(p)});
    box.appendChild(d)})}
const OUT={'picked':'ok','where':'bad','require':'bad','source':'bad','not needed':'muted','not checked':'muted'};
function candidate(c,i){
  const head=el('span');head.appendChild(el('span','n','#'+(i+1)+' '));
  head.appendChild(el('span','key',c.key||'(no key)'));
  head.appendChild(el('span','out '+(OUT[c.outcome]||''),' '+c.outcome+(c.record?' → '+c.record:'')));
  if(c.why)head.appendChild(el('span','why',' · '+c.why));
  return fold(head,'cand',d=>{
    (c.requires||[]).forEach(r=>{
      const h=el('span');h.appendChild(el('span','lbl','require '+r.name+' '));h.appendChild(document.createTextNode(r.call+' '));
      h.appendChild(el('span','out '+(r.held?'ok':'bad'),r.held?'held':'did not hold'));if(r.why)h.appendChild(el('span','why',' · '+r.why));
      d.appendChild(fold(h,'require',x=>{valuesTable(x,r.values);(r.where||[]).forEach(w=>line(x,'cond','where ',w));requests(x,r.requests)}))});
    if(c.value!==undefined)d.appendChild(fold('the candidate','cval',x=>{const j=el('div','json');j.appendChild(node(null,c.value,true));x.appendChild(j)}));
    (c.each||[]).forEach(v=>d.appendChild(visit(v)))})}
function visit(v){
  const head=el('span');head.appendChild(el('b',null,v.node));
  if(v.under)head.appendChild(el('span','muted',' under '+v.under));
  head.appendChild(document.createTextNode('  '+v.call+(v.op?' ('+v.op+')':'')));
  let res,cls='ok';
  if(v.skipped){res='left out: '+v.skipped;cls='bad'}
  else if(v.single){res=v.picked?'1 record':'no record';if(!v.picked)cls='bad'}
  else{res=v.picked+' of '+(v.pick==='all'?'all':v.want)+' picked from '+v.total+' candidates';if(v.picked<v.want||!v.picked&&v.total>0)cls=v.picked?'warn':'bad'}
  head.appendChild(el('span','out '+cls,'  '+res));
  const d=fold(head,'visit',d=>{
    const body=el('div','vbody');d.appendChild(body);
    if(v.skipped)line(body,'cond bad','left out ',v.skipped);
    valuesTable(body,v.values);
    (v.where||[]).forEach(w=>line(body,'cond','where ',w));
    if(v.pick)line(body,'cond','pick ',v.pick+(v.order?' · order: '+v.order:''));
    (v.notes||[]).forEach(n=>line(body,'cond warn','note ',n));
    requests(body,v.requests);
    if(v.candidates&&v.candidates.length){
      const cs=line(body,'cands','candidates ('+v.candidates.length+(v.hidden?' listed, '+v.hidden+' more not listed':'')+')');
      v.candidates.forEach((c,i)=>cs.appendChild(candidate(c,i)))}});
  d.dataset.text=JSON.stringify(v).toLowerCase();
  return d}
// the visits that took nothing or fewer than pick asks for, at the top:
// open one to see its candidates and the where each failed
function short(v){return v.skipped||(v.single?!v.picked:(v.picked<v.want||!v.picked&&v.total>0))}
document.querySelectorAll('.trace').forEach(div=>{
  const shorts=[];(function walk(list){(list||[]).forEach(v=>{if(short(v))shorts.push(v);(v.candidates||[]).forEach(c=>walk(c.each))})})(data.trace);
  if(shorts.length)div.appendChild(fold('Took nothing or fewer than pick asks for ('+shorts.length+')','shorts',d=>shorts.forEach(v=>d.appendChild(visit(v))),true));
  const all=fold('The whole fetch ('+(data.trace||[]).length+' at the top)','whole',d=>(data.trace||[]).forEach(v=>d.appendChild(visit(v))),!shorts.length);
  div.appendChild(all)});
const q=document.getElementById('q'),bad=document.getElementById('bad');
function filter(){const s=q.value.trim().toLowerCase();
  document.querySelectorAll('tr.row').forEach(tr=>{const show=(!bad.checked||tr.classList.contains('bad'))&&(!s||tr.dataset.text.includes(s));
    tr.hidden=!show;const n=tr.nextElementSibling;if(n&&n.classList.contains('body'))n.hidden=!show});
  document.querySelectorAll('details.visit').forEach(d=>{d.hidden=!!s&&!d.dataset.text.includes(s)})}
q.oninput=filter;bad.onchange=filter;
document.getElementById('all').onclick=()=>document.querySelectorAll('details.step').forEach(d=>d.open=true);
document.getElementById('none').onclick=()=>{document.querySelectorAll('details.step').forEach(d=>d.open=false);document.querySelectorAll('tr.body').forEach(r=>{r.previousElementSibling.click()})};
})();
`
