import React, {useMemo, useRef, useState} from 'react';
import {tr} from './locale';
import './ShopStorefront.css';

type Row = Record<string, any>;
type Props = {data:Record<string,Row[]>; canWrite:boolean; error:string; onRefresh:()=>void};
const money = (amount:number) => new Intl.NumberFormat('zh-TW',{style:'currency',currency:'TWD',maximumFractionDigits:0}).format(amount);

export function ShopStorefront({data,canWrite,error:loadError,onRefresh}:Props) {
  const [search,setSearch]=useState(''), [category,setCategory]=useState('');
  const [cart,setCart]=useState<Record<string,{variant:Row;productName:string;quantity:number}>>({});
  const [coupon,setCoupon]=useState('');
  const [address,setAddress]=useState({name:'',city:'',address:'',phone:''});
  const [email,setEmail]=useState('');
  const [busy,setBusy]=useState(false), [error,setError]=useState(''), [notice,setNotice]=useState('');
  const attempt=useRef<{signature:string;key:string}|null>(null);
  const loaded=Array.isArray(data.products), categories=data.categories||[], products=data.products||[], variants=data.variants||[];
  const visible=useMemo(()=>products.filter(p=>(!category||p.categoryId===category)&&(!search||`${p.name} ${p.description||''}`.toLocaleLowerCase().includes(search.toLocaleLowerCase()))),[products,category,search]);
  const lines=Object.values(cart), count=lines.reduce((sum,line)=>sum+line.quantity,0);
  const subtotal=lines.reduce((sum,line)=>sum+Number(line.variant.price)*line.quantity,0);
  const signature=JSON.stringify({items:lines.map(line=>[line.variant.id,line.quantity]).sort((a,b)=>String(a[0]).localeCompare(String(b[0]))),coupon:coupon.trim(),email:email.trim().toLowerCase(),address});
  function adjust(variant:Row,productName:string,delta:number) {
    setCart(current=>{const next={...current}, quantity=(current[variant.id]?.quantity||0)+delta;
      if(quantity<=0) delete next[variant.id];
      else if(quantity<=Math.min(999,Number(variant.stockQuantity)||0)) next[variant.id]={variant,productName,quantity};
      return next;
    });
  }
  async function checkout(event:React.FormEvent<HTMLFormElement>) {
    event.preventDefault(); if(!canWrite||!lines.length)return;
    const key=attempt.current?.signature===signature?attempt.current.key:crypto.randomUUID();
    attempt.current={signature,key};
    setBusy(true);setError('');setNotice('');
    try {
      const response=await fetch('/api/stations/shop/checkout',{method:'POST',credentials:'same-origin',cache:'no-store',headers:{'Content-Type':'application/json'},body:JSON.stringify({items:lines.map(line=>({variantId:line.variant.id,quantity:line.quantity})),couponCode:coupon.trim(),customerEmail:email,shippingAddress:address,idempotencyKey:key})});
      const envelope:any=await response.json();
      if(!response.ok||!envelope.success)throw new Error(envelope.error?.message||`HTTP ${response.status}`);
      const order=envelope.data;
      setNotice(`${tr('Order created')}: ${order.orderNumber} · ${tr('Due')}: ${money(order.totalAmount)} · ${tr('Payment pending')}`);
      setCart({});setCoupon('');attempt.current=null;onRefresh();
    } catch(e) {setError(e instanceof Error?e.message:tr('Checkout failed'));}
    finally {setBusy(false);}
  }
  const orders=data.orders||[], items=data.items||[];
  return <section className="shop-storefront" aria-label={tr('Shop storefront')}>
    <header className="shop-store-hero"><p className="eyebrow">MARKETLANE / SHOP</p><h1>{tr('Everyday essentials, just right.')}</h1><p>{tr('Browse products, build a cart, and check out securely in your workspace.')}</p></header>
    {loadError&&<p className="error" role="alert">{loadError}</p>}{error&&<p className="error" role="alert">{error}</p>}{notice&&<p className="shop-notice" role="status">{notice}</p>}
    <div className="shop-store-grid"><section className="shop-catalog">
      <div className="shop-store-heading"><div><p className="eyebrow">{tr('CATALOG')}</p><h2>{tr('Explore products')}</h2></div><span>{visible.length} {tr('products')}</span></div>
      <div className="shop-filters"><label>{tr('Search products')}<input value={search} onChange={e=>setSearch(e.target.value)} placeholder={tr('Search product names')}/></label><label>{tr('Category')}<select value={category} onChange={e=>setCategory(e.target.value)}><option value="">{tr('All categories')}</option>{categories.map(c=><option key={c.id} value={c.id}>{c.name}</option>)}</select></label></div>
      {!loaded?<p className="empty">{tr('Loading products…')}</p>:!products.length?<p className="empty">{tr('No products are available yet.')}</p>:!visible.length?<p className="empty">{tr('No matching products. Adjust your search.')}</p>:<div className="shop-product-grid">{visible.map(product=>{
        const productVariants=variants.filter(v=>v.productId===product.id&&v.isActive!==0);
        const categoryName=categories.find(c=>c.id===product.categoryId)?.name;
        return <article className="shop-store-product" key={product.id}><div className="shop-product-mark" aria-hidden="true">ML</div><div className="shop-product-copy"><small>{categoryName||tr('Everyday essentials')}</small><h3>{product.name}</h3><p>{product.description}</p></div><div className="shop-product-variants">{productVariants.map(variant=>{
          const available=Math.max(0,Number(variant.stockQuantity)||0), quantity=cart[variant.id]?.quantity||0;
          return <div className="shop-store-variant" key={variant.id}><div><strong>{variant.title}</strong><span>{money(Number(variant.price)||0)} · {tr('In stock')}: {available}</span></div><button type="button" disabled={busy||!canWrite||available<=quantity} onClick={()=>adjust(variant,product.name,1)} aria-label={`${tr('Add')} ${product.name} ${variant.title}`}>+</button></div>;
        })}{!productVariants.length&&<p className="empty">{tr('No available variants')}</p>}</div></article>;
      })}</div>}
    </section>
    <aside className="shop-cart panel"><div className="shop-store-heading"><div><p className="eyebrow">{tr('CHECKOUT')}</p><h2>{tr('Your shopping bag')}</h2></div><span>{count} {tr('items')}</span></div>
      {!lines.length?<p className="empty">{tr('Your cart is empty.')}</p>:<div className="shop-cart-lines">{lines.map(line=><div className="shop-cart-line" key={line.variant.id}><div><strong>{line.productName}</strong><span>{line.variant.title} · {money(Number(line.variant.price)||0)}</span></div><div className="shop-quantity"><button type="button" disabled={busy} aria-label={tr('Decrease quantity')} onClick={()=>adjust(line.variant,line.productName,-1)}>−</button><span>{line.quantity}</span><button type="button" disabled={busy||line.quantity>=Number(line.variant.stockQuantity)} aria-label={tr('Increase quantity')} onClick={()=>adjust(line.variant,line.productName,1)}>+</button></div></div>)}</div>}
      <div className="shop-cart-total"><span>{tr('Merchandise subtotal')}</span><strong>{money(subtotal)}</strong><span>{tr('Shipping')}</span><strong>{money(lines.length?60:0)}</strong><small>{tr('Coupon discount and final total are calculated by the server at checkout.')}</small><small>{tr('Orders are saved to this demo workspace. No payment method is charged.')}</small></div>
      <form className="shop-checkout-form" onSubmit={checkout}><label>{tr('Recipient')}<input required maxLength={300} value={address.name} onChange={e=>setAddress({...address,name:e.target.value})}/></label><label>{tr('City')}<input required maxLength={300} value={address.city} onChange={e=>setAddress({...address,city:e.target.value})}/></label><label>{tr('Delivery address')}<input required maxLength={300} value={address.address} onChange={e=>setAddress({...address,address:e.target.value})}/></label><label>{tr('Phone')}<input required maxLength={300} value={address.phone} onChange={e=>setAddress({...address,phone:e.target.value})}/></label><label>{tr('Email')}<input required type="email" maxLength={254} value={email} onChange={e=>setEmail(e.target.value)}/></label><label>{tr('Coupon code')}<input maxLength={80} value={coupon} onChange={e=>setCoupon(e.target.value)} placeholder="SAVE10"/></label>
        {!canWrite&&<p className="shop-readonly">{tr('Read-only role: checkout is disabled.')}</p>}<button className="shop-submit" disabled={busy||!canWrite||!lines.length}>{busy?tr('Processing…'):tr('Create order')}</button>
      </form>
    </aside></div>
    <section className="shop-order-history"><div className="shop-store-heading"><div><p className="eyebrow">{tr('ACCOUNT')}</p><h2>{tr('My orders')}</h2></div><span>{orders.length}</span></div>
      {!orders.length?<p className="empty">{tr('No orders yet. Your orders will appear here.')}</p>:orders.map(order=><article className="shop-own-order" key={order.id}><header><div><strong>{order.orderNumber}</strong><small>{order.createdAt}</small></div><div className="shop-own-order-status"><span>{tr(order.paymentStatus==='pending'?'Order awaiting payment':order.paymentStatus)}</span><span>{tr(order.fulfillmentStatus)}</span><strong>{money(Number(order.totalAmount)||0)}</strong></div></header>{order.trackingNumber&&<p className="shop-tracking">{tr('Tracking number')}: {order.trackingNumber}</p>}{items.filter(item=>item.orderId===order.id).map(item=><div className="shop-own-order-line" key={item.id}><span>{item.productName} / {item.variantTitle} × {item.quantity}</span><strong>{money(Number(item.lineTotal)||0)}</strong></div>)}</article>)}
    </section>
  </section>;
}
