import React,{useRef,useState} from 'react';
import {tr} from './locale';

type Order=Record<string,any>;
type Props={rows:Order[];returns:Order[];canWrite:boolean;onChange:()=>Promise<void>};

export function ShopShipments({rows,returns,canWrite,onChange}:Props){
 const hasReturns=(orderId:string)=>returns.some(entry=>entry.orderId===orderId);
 const eligible=rows.filter(order=>order.paymentStatus==='paid'&&order.fulfillmentStatus==='unfulfilled'&&!hasReturns(order.id));
 const held=rows.filter(order=>order.fulfillmentStatus==='return_hold'||(order.paymentStatus==='paid'&&order.fulfillmentStatus==='unfulfilled'&&hasReturns(order.id)));
 const [orderId,setOrderId]=useState(''),[trackingNumber,setTrackingNumber]=useState('DEMO-TRACK-001'),[carrier,setCarrier]=useState('Standard Logistics (simulated)'),[busy,setBusy]=useState(false),[notice,setNotice]=useState(''),[error,setError]=useState('');
 const attempt=useRef({signature:'',key:''});
 async function ship(){
  const order=eligible.find(item=>item.id===orderId);
  if(!canWrite||!order||!trackingNumber.trim())return;
  setBusy(true);setError('');setNotice('');
  try{
   const payload={orderId,trackingNumber:trackingNumber.trim(),carrier:carrier.trim()};
   const signature=JSON.stringify(payload);
   if(attempt.current.signature!==signature)attempt.current={signature,key:crypto.randomUUID()};
   const response=await fetch('/api/stations/shop/ship',{method:'POST',credentials:'same-origin',cache:'no-store',headers:{'Content-Type':'application/json'},body:JSON.stringify({...payload,idempotencyKey:attempt.current.key})});
   const value=await response.json() as {success:boolean;data?:{trackingNumber:string;duplicate:boolean};error?:{message:string}};
   if(!response.ok||!value.success)throw new Error(value.error?.message||`HTTP ${response.status}`);
   setNotice(`${order.orderNumber}: ${tr('Simulated shipment recorded')} · ${value.data?.trackingNumber}${value.data?.duplicate?' · '+tr('Retry confirmed'):''}`);
   attempt.current={signature:'',key:''};
   setOrderId('');
   await onChange();
  }catch(cause){setError((cause as Error).message)}finally{setBusy(false)}
 }
 return <section className="panel" aria-label={tr('Shop shipment operations')}>
  <h2>{tr('Simulated shipment')}</h2>
  <p>{tr('Only paid, unfulfilled orders without return activity can be shipped. This operation records a simulated shipment; it does not contact a carrier.')}</p>
  <p>{tr('Order-level shipments cannot safely represent refunded line quantities. Orders with a return before shipment are held for manual resolution.')}</p>
  {held.length>0&&<p role="status">{tr('Orders on return hold')}: {held.map(order=>order.orderNumber).join(', ')}</p>}
  {!canWrite&&<p>{tr('Read-only role: shipment actions are disabled.')}</p>}
  <div className="form-grid">
   <label>{tr('Paid order')}<select value={orderId} disabled={busy||!canWrite} onChange={event=>setOrderId(event.target.value)}><option value="">{tr('Select an order')}</option>{eligible.map(order=><option key={order.id} value={order.id}>{order.orderNumber} · NT${order.totalAmount}</option>)}</select></label>
   <label>{tr('Tracking number')}<input value={trackingNumber} maxLength={80} disabled={busy||!canWrite} onChange={event=>setTrackingNumber(event.target.value)}/></label>
   <label>{tr('Carrier (simulated)')}<input value={carrier} maxLength={80} disabled={busy||!canWrite} onChange={event=>setCarrier(event.target.value)}/></label>
   <button type="button" disabled={busy||!canWrite||!orderId||!trackingNumber.trim()} onClick={()=>void ship()}>{busy?tr('Processing…'):tr('Record shipment')}</button>
  </div>
  {notice&&<p role="status">{notice}</p>}{error&&<p role="alert" className="error">{error}</p>}
  {!eligible.length&&<p className="empty">{tr('No paid unfulfilled orders')}</p>}
 </section>;
}
