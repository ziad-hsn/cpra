import { afterEach, expect, it, vi } from 'vitest';
import { act, cleanup, render, screen } from '@testing-library/react';
import { onlineManager } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { Topbar } from '../src/components/Topbar';
import Overview from '../src/pages/Overview';
import Monitors from '../src/pages/Monitors';
import type { OverviewResponse } from '../src/api/types';

const data=vi.hoisted(()=>({
 overview:{generated:new Date().toISOString(),total:1,disabled:0,by_status:{degraded:1},by_pulse_type:{tls:1},by_code:{yellow:1},up_percent:0,index_capped:false} as OverviewResponse,
 updatedAt: Date.now(),
}));
vi.mock('../src/hooks/queries',()=>({
 useOverview:()=>({data:data.overview,isLoading:false,isError:false,dataUpdatedAt:data.updatedAt,fetchStatus:'idle'}),
 useSystems:()=>({}),
 useIncidents:()=>({data:undefined,isLoading:false,isError:true,error:new Error('HTTP 503')}),
 useQueues:()=>({}),
 useMonitors:()=>({data:undefined,isLoading:false,isError:true,error:new Error('HTTP 503')}),
}));
afterEach(() => {
 cleanup(); onlineManager.setOnline(true); data.updatedAt = Date.now();
 data.overview = {generated: new Date().toISOString(), total: 1, disabled: 0, by_status: {degraded: 1}, by_pulse_type: {tls: 1}, by_code: {yellow: 1}, up_percent: 0, index_capped: false};
});

it('shows simultaneous critical incidents, unavailable checks and suppression',()=>{
 data.overview={...data.overview,total:10,by_status:{incident:2},unsuppressed_unhealthy:2,unavailable:3,suppressed_unhealthy:1};
 render(<MemoryRouter><Topbar /></MemoryRouter>);
 const status=screen.getByRole('status',{name:'Global fleet status'});
 expect(status).toHaveTextContent('2 monitors critical');
 expect(status).toHaveTextContent('3 monitors unavailable');
 expect(status).toHaveTextContent('1 unhealthy monitor — notifications suppressed');
 expect(status).not.toHaveTextContent('All systems operational');
});

it('labels cached healthy observations unavailable while offline', () => {
 data.overview = {...data.overview, total: 1, disabled: 0, by_status: {up: 1}};
 render(<MemoryRouter><Topbar /></MemoryRouter>);
 expect(screen.getByRole('status', {name:'Global fleet status'})).toHaveTextContent('All systems operational');
 act(() => onlineManager.setOnline(false));
 expect(screen.getByRole('status', {name:'Global fleet status'})).toHaveTextContent('Offline — current health unavailable');
 expect(screen.queryByText('All systems operational')).not.toBeInTheDocument();
 expect(screen.getByText(/Saved observations remain visible/)).toBeInTheDocument();
});

it('labels an old cached response stale even before a request fails', () => {
 data.overview = {...data.overview, total: 1, disabled: 0, by_status: {up: 1}};
 data.updatedAt = Date.now() - 31000;
 render(<MemoryRouter><Topbar /></MemoryRouter>);
 expect(screen.getByRole('status', {name:'Global fleet status'})).toHaveTextContent('Observations are stale — current health unavailable');
 expect(screen.queryByText('All systems operational')).not.toBeInTheDocument();
});

it('does not confuse fresh HTTP reception with a progressing controller', () => {
 data.overview = {...data.overview, generated: new Date(Date.now() - 60000).toISOString(), total: 1, disabled: 0, by_status: {up: 1}};
 data.updatedAt = Date.now();
 render(<MemoryRouter><Topbar /></MemoryRouter>);
 expect(screen.getByRole('status', {name:'Global fleet status'})).toHaveTextContent('Observations are stale — current health unavailable');
 expect(screen.getByText(/Controller snapshot/)).toBeInTheDocument();
 expect(screen.queryByText('All systems operational')).not.toBeInTheDocument();
});

it.each(['', 'invalid', new Date(Date.now() + 60000).toISOString()])('does not claim freshness from an absent, invalid or future controller time: %s', generated => {
 data.overview = {...data.overview, generated, total: 1, disabled: 0, by_status: {up: 1}};
 render(<MemoryRouter><Topbar /></MemoryRouter>);
 expect(screen.getByRole('status', {name:'Global fleet status'})).toHaveTextContent('Projection freshness unavailable');
 expect(screen.queryByText('All systems operational')).not.toBeInTheDocument();
});

it('never reports a TLS-warning-only fleet as operational',()=>{
 data.overview = {...data.overview, total: 1, disabled: 0, by_status: {degraded: 1}};
 render(<MemoryRouter><Topbar /></MemoryRouter>);
 expect(screen.getByRole('status',{name:'Global fleet status'})).not.toHaveTextContent('All systems operational');
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

it('does not call a maintenance-suppressed outage critical', () => {
 data.overview = {...data.overview, total: 1, disabled: 0, paused: 0, pending: 0, unavailable: 0, maintenance: 1, suppressed_unhealthy: 1, unsuppressed_unhealthy: 0, by_status: {incident: 1}};
 render(<MemoryRouter><Topbar /></MemoryRouter>);
 const status = screen.getByRole('status', {name: 'Global fleet status'});
 expect(status).toHaveTextContent('1 unhealthy monitor — notifications suppressed');
 expect(status).toHaveTextContent('1 in maintenance; checks continue');
 expect(status).not.toHaveTextContent('critical');
 expect(status).not.toHaveTextContent('All systems operational');
});

it('does not report preparation failures as healthy or as first-check pending', () => {
 data.overview = {...data.overview, total: 1, disabled: 0, paused: 0, pending: 0, unavailable: 1, maintenance: 0, suppressed_unhealthy: 0, unsuppressed_unhealthy: 0, by_status: {unknown: 1}};
 render(<MemoryRouter><Topbar /></MemoryRouter>);
 const status = screen.getByRole('status', {name: 'Global fleet status'});
 expect(status).toHaveTextContent('1 monitor unavailable');
 expect(status).not.toHaveTextContent('Awaiting first');
 expect(status).not.toHaveTextContent('All systems operational');
});
