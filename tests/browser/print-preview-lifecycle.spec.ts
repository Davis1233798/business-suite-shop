import {test,expect} from '@playwright/test';

test('closing print preview aborts its pending dataset request and allows a fresh reopen',async({page})=>{
 await page.goto('/shop/login');await page.getByLabel('Email',{exact:true}).fill('admin@suite.test');await page.getByLabel('Password',{exact:true}).fill('SuiteDemo!2026');await page.getByRole('button',{name:'Sign in',exact:true}).click();await expect(page).toHaveURL(/\/shop\/back/);await expect(page.locator('.dataset').first()).toBeVisible();

 let calls=0;let intercepted!:()=>void;const firstRequestIntercepted=new Promise<void>(resolve=>{intercepted=resolve});
 await page.route('**/api/stations/shop/data?view=back',async route=>{
  calls++;
  if(calls===1){intercepted();await new Promise(resolve=>setTimeout(resolve,1500));try{await route.fulfill({json:{success:true,data:{}}})}catch{/* The browser should already have cancelled this request. */}return}
  await route.fulfill({json:{success:true,data:{}}});
 });

 const requestFailed=page.waitForEvent('requestfailed',request=>request.url().includes('/api/stations/shop/data?view=back'));
 await page.getByRole('button',{name:'Labels and print preview'}).click();await firstRequestIntercepted;
 await page.getByRole('button',{name:'Close print preview'}).click();
 const failedRequest=await requestFailed;expect(failedRequest.failure()?.errorText).toMatch(/abort/i);

 await page.getByRole('button',{name:'Labels and print preview'}).click();
 await expect(page.getByRole('heading',{name:'Print center'})).toBeVisible();
 await expect.poll(()=>calls).toBe(2);
});
