import { expect, it } from 'vitest';
import { actionView } from '../src/api/actions';

const selection = {ruleID:'refused',driver:'webhook',configurationRevision:'config-1',decisionRevision:'decision-1',incidentID:'incident-1',observationID:'5'};
const action = {id:'a',monitorID:'m',state:'unknown',held:true,executorFenced:true};

it('retains the committed rule binding and excludes unapproved response fields',()=>{
  const projected=actionView({...action,recoverySelection:{...selection,privateToken:'not-a-real-token'}},'m');
  expect(projected.recoverySelection).toEqual(selection);
  expect(JSON.stringify(projected)).not.toContain('not-a-real-token');
});
it.each(Object.keys(selection))('rejects incomplete selection binding: %s',key=>{
  expect(()=>actionView({...action,recoverySelection:{...selection,[key]:undefined}},'m')).toThrow();
  expect(()=>actionView({...action,recoverySelection:{...selection,[key]:'a'.repeat(257)}},'m')).toThrow();
  expect(()=>actionView({...action,recoverySelection:{...selection,[key]:'unsafe\u001b'}},'m')).toThrow();
});
