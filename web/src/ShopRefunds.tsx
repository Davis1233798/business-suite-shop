import React,{useRef,useState} from 'react';
import {tr} from './locale';

type Row=Record<string,any>;
type Props={orders:Row[];items:Row[];returns:Row[];canWrite:boolean;onChange:()=>Promise<void>};

export function ShopRefunds({orders,items,returns,canWrite,onChange}:Props){
 const paidOrders=orders.filter(order=>order.paymentStatus==='paid');
 const [orderId,setOrderId]=useState(''),[itemId,setItemId]=useState(''),[quantity,setQuantity]=useState(1),[reason,setReason]=useState('Customer return'),[busy,setBusy]=useState(false),[notice,setNotice]=useState(''),[error,setError]=useState('');
 const attempt=useRef({signature:'',key:''});
 const order=paidOrders.find(row=>row.id===orderId);
 const orderItems=items.filter(item=>item.orderId===orderId);
 const item=orderItems.find(row=>row.id===itemId);
 const alreadyReturned=item?returns.filter(entry=>entry.orderItemId===item.id).reduce((sum,entry)=>sum+Number(entry.quantity||0),0):0;
 const remaining=Math.max(0,Number(item?.quantity||0)-alreadyReturned);
 async function refund(){
  if(!canWrite||!order||!item||!Number.isSafeInteger(quantity)||quantity<1||quantity>remaining||!reason.trim())return;
  setBusy(true);setError('');setNotice('');
  try{
   const payload={orderId:order.id,orderItemId:item.id,quantity,reason:reason.trim()};
   const signature=JSON.stringify(payload);
   if(attempt.current.signature!==signature)attempt.current={signature,key:crypto.randomUUID()};
   const response=await fetch('/api/stations/shop/refund',{method:'POST',credentials:'same-origin',cache:'no-store',headers:{'Content-Type':'application/json'},body:JSON.stringify({...payload,idempotencyKey:attempt.current.key})});
   const value=await response.json() as {success:boolean;data?:{refundAmount:number;orderRefundedAmount:number;fulfillmentStatus:string;duplicate:boolean};error?:{message:string}};
   if(!response.ok||!value.success)throw new Error(value.error?.message||`HTTP ${response.status}`);
   setNotice(`${order.orderNumber}: ${tr('Recorded simulated refund')} NT$${value.data?.refundAmount} · ${tr('Order refund total')} NT$${value.data?.orderRefundedAmount} · ${tr('Fulfillment status')}: ${tr(value.data?.fulfillmentStatus||'')}${value.data?.duplicate?' · '+tr('Retry confirmed'):''}`);
   attempt.current={signature:'',key:''};
   setQuantity(1);
   await onChange();
  }catch(cause){setError((cause as Error).message)}finally{setBusy(false)}
 }
 return <section className="panel" aria-label={tr('Shop return and refund operations')}>
  <h2>{tr('Simulated returns and refunds')}</h2>
  <p>{tr('Refunds are calculated from paid order item lines and exclude shipping. Returned units are added back to stock. No payment provider is contacted and no money is transferred.')}</p>
  <p>{tr('An unshipped order with any return is put on hold because this order-level shipment flow cannot represent individual returned quantities. Returning every merchandise item cancels fulfillment; a return after shipment preserves shipped status and tracking.')}</p>
  <p>{tr('Full merchandise returns leave payment status paid because shipping is excluded from the refund.')}</p>
  {!canWrite&&<p>{tr('Administrator role required: refund actions are disabled.')}</p>}
  <div className="form-grid">
   <label>{tr('Paid order')}<select value={orderId} disabled={busy||!canWrite} onChange={event=>{setOrderId(event.target.value);setItemId('');setQuantity(1)}}><option value="">{tr('Select an order')}</option>{paidOrders.map(row=><option key={row.id} value={row.id}>{row.orderNumber} · {row.fulfillmentStatus} · NT${row.totalAmount}</option>)}</select></label>
   <label>{tr('Order item')}<select value={itemId} disabled={busy||!canWrite||!order} onChange={event=>{setItemId(event.target.value);setQuantity(1)}}><option value="">{tr('Select an order item')}</option>{orderItems.map(row=><option key={row.id} value={row.id}>{row.productName} · {row.variantTitle} · {tr('Remaining to return')} {Math.max(0,Number(row.quantity)-returns.filter(entry=>entry.orderItemId===row.id).reduce((sum,entry)=>sum+Number(entry.quantity||0),0))}</option>)}</select></label>
   <label>{tr('Return quantity')}<input type="number" min={1} max={remaining||1} value={quantity} disabled={busy||!canWrite||!item||remaining<1} onChange={event=>setQuantity(Number(event.target.value))}/></label>
   <label>{tr('Return reason')}<input value={reason} maxLength={500} disabled={busy||!canWrite} onChange={event=>setReason(event.target.value)}/></label>
   <button type="button" disabled={busy||!canWrite||!order||!item||remaining<1||quantity<1||quantity>remaining||!reason.trim()} onClick={()=>void refund()}>{busy?tr('Processing…'):tr('Record return and refund')}</button>
  </div>
  {order&&<p>{tr('Order payment and fulfillment status remain unchanged')}: {order.paymentStatus} · {order.fulfillmentStatus}{order.trackingNumber?` · ${tr('Tracking number')}: ${order.trackingNumber}`:''}</p>}
  {notice&&<p role="status">{notice}</p>}{error&&<p role="alert" className="error">{error}</p>}
  {!paidOrders.length&&<p className="empty">{tr('No paid orders available for return')}</p>}
 </section>;
}
