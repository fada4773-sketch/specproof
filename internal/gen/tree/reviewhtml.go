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
const q=document.getElementById('q'),bad=document.getElementById('bad');
function filter(){const s=q.value.trim().toLowerCase();
  document.querySelectorAll('tr.row').forEach(tr=>{const show=(!bad.checked||tr.classList.contains('bad'))&&(!s||tr.dataset.text.includes(s));
    tr.hidden=!show;const n=tr.nextElementSibling;if(n&&n.classList.contains('body'))n.hidden=!show})}
q.oninput=filter;bad.onchange=filter;
document.getElementById('all').onclick=()=>document.querySelectorAll('details.step').forEach(d=>d.open=true);
document.getElementById('none').onclick=()=>{document.querySelectorAll('details.step').forEach(d=>d.open=false);document.querySelectorAll('tr.body').forEach(r=>{r.previousElementSibling.click()})};
})();
`
