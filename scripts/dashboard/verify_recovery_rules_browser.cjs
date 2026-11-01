'use strict';
const { chromium } = require(process.env.CPRA_BROWSER_MODULE);
const path = require('node:path');
const { auditAccessibility } = require('./accessibility.cjs');
const origin = process.env.CPRA_BROWSER_ORIGIN;
const operator = 'main-operator-fixture-012345678901234567890123456789';
function check(value, message) { if (!value) throw new Error(message); }
(async () => {
 const browser = await chromium.launch({headless: true, executablePath: process.env.CPRA_BROWSER_EXECUTABLE, args: [`--ignore-certificate-errors-spki-list=${process.env.CPRA_BROWSER_CERT_PIN}`]});
 try {
  const page = await browser.newPage({viewport: {width: 1440, height: 1080}});
  page.setDefaultTimeout(20000);
  const writes=[],errors=[],accessibility=[];
  page.on('pageerror',error=>errors.push(error.message));
  page.on('dialog',dialog=>dialog.accept());
  page.on('request',request=>{if(request.url().startsWith(origin+'/api/v2/') && request.method()!=='GET') writes.push({method:request.method(), path:new URL(request.url()).pathname, precondition:request.headers()['if-match']});});
  await page.goto(origin+'/monitor-configurations/browser-rules');
  await page.getByLabel('Bearer token').fill(operator);
  await page.getByRole('button',{name:'Sign in',exact:true}).click();
  await page.getByRole('button',{name:'Edit monitor',exact:true}).click();
  await page.getByLabel('Rule 2 http statuses',{exact:false}).fill('503');
  await page.getByRole('button',{name:'Move rule 2 up',exact:true}).click();
  check(await page.getByLabel('Rule 1 ID',{exact:true}).inputValue()==='recover-unavailable','rule ordering changed another rule');
  await auditAccessibility(page,'recovery-rule-editor',accessibility);
  await page.getByRole('button',{name:'Save',exact:true}).click();
  await page.getByText('Saved durably and applied by the controller.',{exact:true}).waitFor();
  check(writes.length===1&&!!writes[0].precondition,'rule edit lacked conditional write');
  let counts=await(await fetch(process.env.CPRA_BROWSER_TARGET+'/count')).json();
  check(counts.effects===0,'editing disabled rules invoked recovery');
  await page.getByRole('link',{name:'View monitoring and controls',exact:true}).click();
  await page.getByRole('button',{name:'Enable',exact:true}).click();
  await page.getByRole('dialog',{name:'Enable',exact:true}).getByRole('button',{name:'Enable',exact:true}).click();
  await page.getByText('Saved durably and applied by the controller.',{exact:true}).waitFor();
  for(let i=0;i<100;i++) {
   counts=await(await fetch(process.env.CPRA_BROWSER_TARGET+'/count')).json();
   if(counts.effects===1)break;
   await page.waitForTimeout(100);
  }
  check(counts.effects===1,'qualified rule did not reach the designated receiver');
  await page.goto(origin+'/monitor-configurations/browser-rules');
  // Bearers are intentionally held only in memory; a full navigation starts
  // a fresh authenticated session rather than persisting the credential.
  await page.getByLabel('Bearer token').fill(operator);
  await page.getByRole('button',{name:'Sign in',exact:true}).click();
  await page.getByRole('region',{name:'Intervention selection'}).waitFor();
  check((await page.getByRole('region',{name:'Intervention selection'}).textContent()).includes('recover-unavailable'),'selected rule explanation missing');
  await page.screenshot({path:path.join(process.env.CPRA_BROWSER_EVIDENCE_DIR,'recovery-rules-selected.png'),fullPage:true});
  await page.setViewportSize({width:390,height:844});
  await page.getByRole('button',{name:'Edit monitor',exact:true}).click();
  await page.evaluate(()=>{document.documentElement.style.fontSize='200%';});
  await auditAccessibility(page,'recovery-rules-mobile',accessibility);
  check(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth+1),'mobile rules overflow');
  await page.screenshot({path:path.join(process.env.CPRA_BROWSER_EVIDENCE_DIR,'recovery-rules-mobile.png'),fullPage:true});
  await page.waitForTimeout(1000);
  counts=await(await fetch(process.env.CPRA_BROWSER_TARGET+'/count')).json();
  check(counts.effects===1,'uncertain HTTP500 was invoked again');
  check(errors.length===0,'browser runtime error');
  check(!(await page.content()).includes(operator),'bearer entered DOM');
  check(!(await page.content()).includes(process.env.CPRA_BROWSER_TARGET+'/recover'),'resolved credential entered DOM');
  console.log(JSON.stringify({writes,checks:counts.checks,effects:counts.effects,accessibility}));
 } finally {await browser.close();}
})().catch(error=>{console.error(error.message);process.exitCode=1;});
