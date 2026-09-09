import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import Settings from '../src/pages/Settings';
import MonitorDetail from '../src/pages/MonitorDetail';
import Alerts from '../src/pages/Alerts';
import { DataTable } from '../src/components/DataTable';
import { FilterBar } from '../src/components/FilterBar';

vi.mock('../src/hooks/queries', () => ({
 useIncidents:()=>({data:{count:0,incidents:[]},isLoading:false,isError:false}),
 useConfig:()=>({data:{queue_capacity:10,batch_size:1,alert_cooldown:300e9,recovery_bypass:false,use_adaptive_queue:false,queue_type:'hybrid'},isLoading:false,isError:false}),
 useHealth:()=>({data:{status:'ok'},isLoading:false,isError:true,error:new Error('server unavailable')}),
 useMonitor:()=>({data:{id:1,name:'db',pulse_type:'tcp',status:'incident',incident:true,pending_code:'red',active_codes:['red'],next_check:new Date(Date.now()+120_000).toISOString(),last_check:'',last_success:'',consecutive_failures:1},isLoading:false,isError:false}),
}));
afterEach(cleanup);

it('does not report healthy after the latest health request fails',()=>{
 render(<Settings />);
 expect(screen.queryByText('Server is healthy')).not.toBeInTheDocument();
});

it('does not invent an alert cooldown from the next pulse time',()=>{
 render(<MemoryRouter><MonitorDetail /></MemoryRouter>);
 expect(screen.queryByRole('timer')).not.toBeInTheDocument();
 expect(screen.getByText('pending delivery')).toBeInTheDocument();
});

vi.mock('@tanstack/react-virtual',()=>({useVirtualizer:()=>({getTotalSize:()=>50,getVirtualItems:()=>[{index:0,start:0,size:50}]})}));

it('does not infer healthy fleet from zero open incidents',()=>{
 render(<MemoryRouter><Alerts /></MemoryRouter>);
 expect(screen.queryByText('Your fleet is healthy — all quiet.')).not.toBeInTheDocument();
});

it('does not format an unperformed check as a real clock time',()=>{
 const zero='0001-01-01T00:00:00Z';
 render(<DataTable data={[{id:1,name:'new monitor',pulse_type:'http',status:'unknown',incident:false,pending_code:'',consecutive_failures:0,last_check:zero,last_success:zero,next_check:zero,active_codes:[]}]} onRowClick={()=>{}}/>);
 const renderedZero=new Date(zero).toLocaleTimeString('en-US',{hour:'2-digit',minute:'2-digit',second:'2-digit',hour12:false});
 expect(screen.getByRole('row')).not.toHaveTextContent(renderedZero);
});

it('allows filtering a supported DNS check type',()=>{
 render(<FilterBar pulseTypes={['dns', 'tls', 'redis']} filters={{status:'',type:'',code:'',q:''}} onChange={()=>{}}/>);
 expect(screen.queryByRole('option',{name:'DNS'})).toBeInTheDocument();
});
