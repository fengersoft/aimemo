const q=document.getElementById('q'),cat=document.getElementById('category'),refresh=document.getElementById('refresh'),list=document.getElementById('list'),empty=document.getElementById('empty'),article=document.getElementById('article');
const title=document.getElementById('title'),meta=document.getElementById('meta'),content=document.getElementById('content'),problem=document.getElementById('problem'),solution=document.getElementById('solution'),pitfall=document.getElementById('pitfall'),example=document.getElementById('example'),tags=document.getElementById('tags'),raw=document.getElementById('raw');
const wsEl=document.getElementById('ws'),prev=document.getElementById('prev'),next=document.getElementById('next'),pageinfo=document.getElementById('pageinfo');
const copyMd=document.getElementById('copyMd'),copyJson=document.getElementById('copyJson'),copyPrompt=document.getElementById('copyPrompt');
let items=[],total=0,limit=50,offset=0,cur=null;

async function loadCats(){
  const r=await fetch('/api/stats');const j=await r.json();
  if(j.data){
    wsEl.textContent = 'workspace: ' + (j.data.stats && j.data.stats.workspace!==undefined ? (j.data.stats.workspace===''?'(空)':j.data.stats.workspace) : '');
    cat.innerHTML='<option value="">全部分类</option>'+((j.data.categories)||[]).map(c=>`<option value="${c.name}">${c.name} (${c.count})</option>`).join('');
  }
}
function syncURL(){
  const p=new URLSearchParams();
  if(q.value) p.set('q',q.value);
  if(cat.value) p.set('category',cat.value);
  if(offset>0) p.set('offset',String(offset));
  if(limit!==50) p.set('limit',String(limit));
  const s=p.toString();
  history.replaceState({q:q.value,cat:cat.value,offset,limit},'', s?('?'+s):location.pathname);
}
async function load(){
  const params=new URLSearchParams({limit:String(limit),offset:String(offset),q:q.value,category:cat.value});
  const r=await fetch('/api/list?'+params);const j=await r.json();
  items=(j.data&&j.data.items)||[]; total=(j.data&&j.data.total)||0;
  if(items.length===0){
    list.innerHTML='<div style="padding:.8rem;color:#9ca3af">无匹配结果</div>';
  }else{
    list.innerHTML=items.map((it,i)=>`<div class="item" data-i="${i}"><div class="title">${esc(it.title)}</div><div class="meta">${it.category}/${it.type} #${it.id} imp:${it.importance}</div></div>`).join('');
    list.querySelectorAll('.item').forEach(el=>el.onclick=()=>show(+el.dataset.i));
  }
  const page=Math.floor(offset/limit)+1; const pages=Math.max(1,Math.ceil(total/limit));
  pageinfo.textContent=`${page}/${pages} (共${total}条)`;
  prev.disabled=offset<=0; next.disabled=offset+limit>=total;
  if(offset===0 && items.length===0){ empty.hidden=false; article.hidden=true; }
  syncURL();
}
function show(i){
  list.querySelectorAll('.item').forEach((el,idx)=>el.classList.toggle('active',idx===i));
  const m=items[i]; if(!m){cur=null;return;}
  cur=m; empty.hidden=true;article.hidden=false;
  title.textContent=m.title;
  meta.textContent=`ID:${m.id} | 分类:${m.category} | 类型:${m.type} | 重要度:${m.importance} | 置信度:${m.confidence} | 工作区:${m.workspace} | 更新时间:${m.updated_at}`;
  content.textContent=m.content||'';
  problem.textContent=m.problem||'';
  solution.textContent=m.solution||'';
  pitfall.textContent=m.pitfall||'';
  example.textContent=m.example||'';
  tags.innerHTML=(m.tags||[]).map(t=>`<span class="tag">${esc(t)}</span>`).join('');
  raw.textContent=JSON.stringify(m,null,2);
  const p=new URLSearchParams(location.search); p.set('id',m.id); history.replaceState(null,'','?'+p.toString());
}
function esc(s){return (s||'').replace(/[&<>"']/g,c=>({ '&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;' }[c]));}
q.oninput=()=>{offset=0;load();};cat.onchange=()=>{offset=0;load();};refresh.onclick=()=>{offset=0;load();};
prev.onclick=()=>{offset=Math.max(0,offset-limit);load();};
next.onclick=()=>{offset+=limit;load();};
copyMd.onclick=()=>{if(!cur)return; const md=`# ${cur.title}

- ID: ${cur.id}
- 分类: ${cur.category}
- 类型: ${cur.type}
- 重要度: ${cur.importance}
- 标签: ${(cur.tags||[]).join(', ')}

## 内容

${cur.content||''}

## 问题

${cur.problem||''}

## 解决方案

${cur.solution||''}

## 踩坑与注意事项

${cur.pitfall||''}

## 示例

${cur.example||''}
`; navigator.clipboard.writeText(md);};
copyJson.onclick=()=>{if(!cur)return; navigator.clipboard.writeText(JSON.stringify(cur,null,2));};
copyPrompt.onclick=()=>{if(!cur)return; const p=`经验库 #${cur.id} [${cur.category}] ${cur.title}

${cur.content||''}

问题：${cur.problem||''}

解决方案：${cur.solution||''}

注意：${cur.pitfall||''}

示例：${cur.example||''}`; navigator.clipboard.writeText(p);};
const u=new URLSearchParams(location.search); if(u.get('q')) q.value=u.get('q'); if(u.get('category')) cat.value=u.get('category'); if(u.get('offset')) offset=parseInt(u.get('offset'))||0; if(u.get('limit')) limit=parseInt(u.get('limit'))||50;
loadCats();load().then(async()=>{
  const id=new URLSearchParams(location.search).get('id');
  if(id){ const r=await fetch('/api/get?id='+encodeURIComponent(id)); const j=await r.json(); if(j.ok&&j.data){ empty.hidden=true;article.hidden=false; const m=j.data; cur=m; title.textContent=m.title; meta.textContent=`ID:${m.id} | 分类:${m.category} | 类型:${m.type} | 重要度:${m.importance} | 置信度:${m.confidence} | 工作区:${m.workspace} | 更新时间:${m.updated_at}`; content.textContent=m.content||''; problem.textContent=m.problem||''; solution.textContent=m.solution||''; pitfall.textContent=m.pitfall||''; example.textContent=m.example||''; tags.innerHTML=(m.tags||[]).map(t=>`<span class="tag">${esc(t)}</span>`).join(''); raw.textContent=JSON.stringify(m,null,2);} }
});
