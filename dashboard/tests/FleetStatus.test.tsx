import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { Topbar } from '../src/components/Topbar';
import Overview from '../src/pages/Overview';
import Monitors from '../src/pages/Monitors';
import type { OverviewResponse } from '../src/api/types';

const data=vi.hoisted(()=>({
 overview:{generated:new Date().toISOString(),total:1,disabled:0,by_status:{degraded:1},by_pulse_type:{tls:1},by_code:{yellow:1},up_percent:0,index_capped:false} as OverviewResponse,
}));
vi.mock('../src/hooks/queries',()=>({
 useOverview:()=>({data:data.overview,isLoading:false,isError:false}),
 useSystems:()=>({}),
 useIncidents:()=>({data:undefined,isLoading:false,isError:true,error:new Error('HTTP 503')}),
 useQueues:()=>({}),
 useMonitors:()=>({data:undefined,isLoading:false,isError:true,error:new Error('HTTP 503')}),
}));
afterEach(cleanup);

it('never reports a TLS-warning-only fleet as operational',()=>{
 render(<MemoryRouter><Topbar /></MemoryRouter>);
 expect(screen.getByRole('banner',{name:'Global fleet status'})).not.toHaveTextContent('All systems operational');
});
it('includes degraded monitors in the status distribution',()=>{
 render(<MemoryRouter><Overview /></MemoryRouter>);
 expect(screen.queryByText('Degraded')).toBeInTheDocument();
 expect(screen.queryByText('all active monitors operational')).not.toBeInTheDocument();
});
it('does not call the unavailable incident index empty',()=>{
 data.overview={...data.overview,total:1000001,by_status:{up:999994,incident:7},index_capped:true};
 render(<MemoryRouter><Overview /></MemoryRouter>);
 expect(screen.queryByText('No active incidents')).not.toBeInTheDocument();
 expect(screen.queryByText('0 active')).not.toBeInTheDocument();
});

it('does not claim zero monitors when the index is unavailable',()=>{
 data.overview={...data.overview,total:1000001,by_status:{up:999994,incident:7},index_capped:true};
 render(<MemoryRouter><Monitors /></MemoryRouter>);
 expect(screen.queryByText('Showing 0–0 of 0')).not.toBeInTheDocument();
});
