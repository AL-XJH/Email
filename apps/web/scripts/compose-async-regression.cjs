// Run against a VM-built web bundle; every API is intercepted, no mail is delivered.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright')
const assert = require('node:assert/strict')
const fs = require('node:fs')
const base = process.env.BASE_URL || 'http://192.168.100.10:18089'
const permissions = ['mail.access','mail.messages.read','mail.messages.send','mail.messages.drafts','mail.messages.schedule','mail.messages.organize','mail.attachments.download','mail.signatures.manage']
const mailbox = {id:'mb',userId:'u',address:'user@example.test',status:'active'}
const message = (id,folder='Drafts') => ({id,folder,mailboxId:'mb',mailboxAddress:mailbox.address,from:'sender@example.test',to:[mailbox.address],cc:[],bcc:[],subject:`信件${id}`,snippet:`正文${id}`,bodyText:`正文${id}`,bodyHtml:`<p>正文${id}</p>`,isRead:true,attachments:[],labels:[],receivedAt:'2026-11-26T00:00:00Z'})
function latch(){let release,enter;const wait=new Promise(r=>release=r),started=new Promise(r=>enter=r);return {release,enter,wait,started}}
let currentFixture
async function fixture(browser,hook){
 const ctx=await browser.newContext({viewport:{width:1440,height:1000}}),net=[],errors=[]
 ctx.setDefaultTimeout(7000)
 await ctx.route('**/api/**',async r=>{
  const q=r.request(),u=new URL(q.url()),p=u.pathname;net.push({path:p,method:q.method(),body:q.postData()})
  if(hook && await hook(r,p))return
  let b={items:[]}
  if(p==='/api/me')b={user:{id:'u',role:'user',email:mailbox.address,permissions},preferences:{}}
  if(p.includes('mailboxes'))b={items:[mailbox]}
  if(p==='/api/public/settings')b={siteTitle:'回归测试',externalImapEnabled:false}
  if(p.includes('signature'))b={signature:null}
  if(p==='/api/mail/folders')b={items:['Inbox','Drafts','Sent'].map(name=>({id:name,name,total:2,unread:0}))}
  if(p==='/api/mail/messages'){const folder=u.searchParams.get('folder');b={items:['A','B'].map(id=>({...message(id,folder),bodyText:undefined,bodyHtml:undefined})),total:2}}
  if(/^\/api\/mail\/messages\/[AB]$/.test(p))b=message(p.slice(-1))
  if(p.startsWith('/api/mail/drafts')&&q.method()==='POST')b={...message('saved'),...q.postDataJSON(),id:'saved'}
  if(p==='/api/mail/send')b=message('sent','Sent')
  if(p==='/api/mail/schedule-send')b={id:'sched',sendAt:q.postDataJSON().sendAt,status:'pending'}
  if(p==='/api/events'){await r.abort();return}
  await r.fulfill({status:200,contentType:'application/json',body:JSON.stringify(b)})
 })
 const p=await ctx.newPage();p.on('pageerror',e=>errors.push(e.message));await p.goto(base);await p.getByRole('button',{name:'写邮件',exact:true}).waitFor()
 currentFixture={ctx,p,net,errors};return currentFixture
}
async function compose(p,subject='新写信'){await p.getByRole('button',{name:'写邮件',exact:true}).click();await p.locator('[name=to]').fill('receiver@example.test');await p.locator('[name=subject]').fill(subject);await p.locator('.tiptap').fill('保留我的正文')}
async function action(p,id,kind){if(kind==='draft'){await p.getByRole('button',{name:/草稿箱/}).click();await p.getByRole('button',{name:new RegExp(`信件${id}`)}).click()}else{await p.getByRole('button',{name:new RegExp(`信件${id}`)}).click({button:'right'});await p.getByRole('menu').getByRole('button',{name:kind,exact:true}).click()}}
async function schedule(p){await p.getByRole('button',{name:'定时',exact:true}).click();await p.getByRole('button',{name:'确认定时',exact:true}).click()}
// 失败后会弹 toast：第一次 Esc 被 toast 消费，必须显式关掉定时弹窗再继续。
async function closeScheduleDialog(p){const d=p.getByRole('dialog').filter({hasText:'定时发送'});if(await d.count()){await d.getByRole('button',{name:'取消',exact:true}).click();await p.waitForTimeout(200)}}
const sleep=p=>p.waitForTimeout(250)
;(async()=>{
 const browser=await chromium.launch({headless:true,executablePath:process.env.CHROMIUM_PATH,args:['--no-sandbox']}),results=[]
 browser.on('context',c=>c.setDefaultTimeout(7000))
 async function test(name,fn){if(process.env.TEST_FILTER&&!name.includes(process.env.TEST_FILTER))return;let f;currentFixture=null;try{f=await fn();assert.deepEqual(f.errors,[]);results.push({name,pass:true})}catch(e){results.push({name,pass:false,error:e.message,text:await currentFixture?.p.locator('body').innerText(),net:currentFixture?.net})}finally{await (f||currentFixture)?.ctx.close()}console.log(JSON.stringify(results.at(-1)))}
 for(const [kind,target,attachment] of [['draft','new',false],['draft','B',false],['draft','new',true],['回复','new',false],['全部回复','new',false],['转发','new',false],['转发','new',true]]){
 await test(`late-${kind}-${attachment?'attachment':'detail'}-keeps-${target}`,async()=>{
  const gate=latch();const f=await fixture(browser,async(r,path)=>{
   if(path==='/api/mail/messages/A'){
    if(!attachment){gate.enter();await gate.wait}
    const m=message('A',kind==='draft'?'Drafts':'Inbox');if(attachment)m.attachments=[{id:'att',filename:'test.txt',contentType:'text/plain',sizeBytes:4}]
    await r.fulfill({contentType:'application/json',body:JSON.stringify(m)});return true
   }
   if(path.includes('attachments')){gate.enter();await gate.wait;await r.fulfill({body:'test'});return true}
  })
  await action(f.p,'A',kind);await gate.started
  if(target==='new')await compose(f.p);else await action(f.p,'B','draft')
  await f.p.locator('[name=subject]').waitFor();const expected=await f.p.locator('[name=subject]').inputValue();gate.release();await sleep(f.p)
  assert.equal(await f.p.locator('[name=subject]').inputValue(),expected);return f
 })}
 await test('schedule-pending-Esc-locks-send-and-form-submit',async()=>{
  const g=latch(),f=await fixture(browser,async(r,p)=>{if(p==='/api/mail/schedule-send'){g.enter();await g.wait;await r.fulfill({status:503,contentType:'application/json',body:'{"error":"retry"}'});return true}})
  await compose(f.p);await schedule(f.p);await g.started;await f.p.keyboard.press('Escape');await sleep(f.p)
  assert.equal(await f.p.getByRole('button',{name:'发送',exact:true}).isDisabled(),true)
  // Implicit Enter submits the real form even if a button guard is incomplete.
  await f.p.locator('[name=subject]').press('Enter');await sleep(f.p);assert.equal(f.net.filter(x=>x.path==='/api/mail/send').length,0)
  g.release();await sleep(f.p);await f.p.getByRole('button',{name:'发送',exact:true}).click();await sleep(f.p);assert.equal(f.net.filter(x=>x.path==='/api/mail/send').length,1);return f
 })
 for(const mode of ['send','schedule']){
 await test(`${mode}-encoding-failure-releases-lock-and-autosaves`,async()=>{
  const f=await fixture(browser);await compose(f.p)
  await f.p.locator('input[type=file]').setInputFiles({name:'test.txt',mimeType:'text/plain',buffer:Buffer.from('data')})
  await f.p.evaluate(()=>{const original=File.prototype.arrayBuffer;File.prototype.arrayBuffer=function(){File.prototype.arrayBuffer=original;return Promise.reject(new Error('encoding failed'))}})
  if(mode==='send')await f.p.getByRole('button',{name:'发送',exact:true}).click();else await schedule(f.p)
  await sleep(f.p);if(mode==='schedule')await closeScheduleDialog(f.p)
  await f.p.locator('[name=subject]').fill('失败后继续编辑');await f.p.getByRole('button',{name:'取消',exact:true}).click();await sleep(f.p)
  assert.ok(f.net.some(x=>x.path.startsWith('/api/mail/drafts')&&x.method==='POST'&&JSON.parse(x.body).subject==='失败后继续编辑'))
  await compose(f.p);if(mode==='send')await f.p.getByRole('button',{name:'发送',exact:true}).click();else await schedule(f.p);await sleep(f.p)
  assert.equal(f.net.filter(x=>x.path===(mode==='send'?'/api/mail/send':'/api/mail/schedule-send')).length,1);return f
 })
 await test(`${mode}-network-failure-retries-and-autosaves`,async()=>{
  let count=0;const endpoint=mode==='send'?'/api/mail/send':'/api/mail/schedule-send'
  const f=await fixture(browser,async(r,p)=>{if(p===endpoint&&++count===1){await r.fulfill({status:503,contentType:'application/json',body:'{"error":"retry"}'});return true}})
  await compose(f.p);if(mode==='send')await f.p.getByRole('button',{name:'发送',exact:true}).click();else await schedule(f.p);await sleep(f.p)
  if(mode==='schedule')await closeScheduleDialog(f.p);await f.p.locator('[name=subject]').fill('网络失败后编辑')
  await f.p.waitForTimeout(5200);assert.ok(f.net.some(x=>x.path.startsWith('/api/mail/drafts')&&x.method==='POST'))
  if(mode==='send')await f.p.getByRole('button',{name:'发送',exact:true}).click();else await schedule(f.p);await sleep(f.p);assert.equal(count,2);return f
 })
 await test(`${mode}-encoding-double-submit-mutually-exclusive`,async()=>{
  const g=latch(),f=await fixture(browser,async(r,p)=>{if(p===(mode==='send'?'/api/mail/send':'/api/mail/schedule-send')){g.enter();await g.wait;await r.fulfill({status:503,contentType:'application/json',body:'{"error":"retry"}'});return true}})
  await compose(f.p);await f.p.locator('input[type=file]').setInputFiles({name:'test.txt',mimeType:'text/plain',buffer:Buffer.from('data')})
  await f.p.evaluate(()=>{const original=File.prototype.arrayBuffer;window.releaseEncoding=null;File.prototype.arrayBuffer=function(){return new Promise(resolve=>window.releaseEncoding=()=>{File.prototype.arrayBuffer=original;resolve(original.call(this))})}})
  if(mode==='send'){await f.p.getByRole('button',{name:'发送',exact:true}).dblclick();await f.p.locator('[name=subject]').press('Enter')}else{await f.p.getByRole('button',{name:'定时',exact:true}).click();await f.p.getByRole('button',{name:'确认定时',exact:true}).dblclick()}
  await f.p.evaluate(()=>window.releaseEncoding());await g.started
  if(mode==='schedule'){await f.p.keyboard.press('Escape');await f.p.locator('[name=subject]').press('Enter')}else await f.p.locator('[name=subject]').press('Enter')
  await sleep(f.p);assert.equal(f.net.filter(x=>['/api/mail/send','/api/mail/schedule-send'].includes(x.path)).length,1);g.release();return f
 })}
 await test('late-send-A-does-not-close-B',async()=>{
  const g=latch(),f=await fixture(browser,async(r,p)=>{if(p==='/api/mail/send'){g.enter();await g.wait;await r.fulfill({contentType:'application/json',body:JSON.stringify(message('sent','Sent'))});return true}})
  await compose(f.p,'发送A');await f.p.getByRole('button',{name:'发送',exact:true}).click();await g.started;await f.p.getByRole('button',{name:'取消',exact:true}).click();await compose(f.p,'新写信B');g.release();await sleep(f.p)
  assert.equal(await f.p.locator('[name=subject]').inputValue(),'新写信B');assert.equal(await f.p.locator('.tiptap').innerText(),'保留我的正文');return f
 })
 await browser.close();if(process.env.RESULT_FILE)fs.writeFileSync(process.env.RESULT_FILE,JSON.stringify(results,null,2));console.log(`${results.filter(x=>x.pass).length}/${results.length} passed`);if(results.some(x=>!x.pass))process.exitCode=1
})().catch(e=>{console.error(e);process.exitCode=1})
