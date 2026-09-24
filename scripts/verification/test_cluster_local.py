import unittest

import cluster_local
import observe_icmp
import observe_effect


class ClusterFixtureTests(unittest.TestCase):
    def test_only_designated_kind_node_allowed(self):
        row = {'Name': '/cpra-test-control-plane', 'State': {'Running': True},
               'Config': {'Labels': {'io.x-k8s.kind.cluster': 'cpra-test'}}}
        cluster_local.validate_node('cpra-test-control-plane', row)
        for name, candidate in [('thndr-control-plane', row), ('cpra-test-control-plane', dict(row, State={'Running': False})),
                                ('cpra-test-control-plane', dict(row, Config={'Labels': {'io.x-k8s.kind.cluster': 'thndr'}}))]:
            with self.assertRaises(ValueError):
                cluster_local.validate_node(name, candidate)

    def test_manifest_scopes_changes_and_records_actual_effects(self):
        config = cluster_local.build_config('cpra-test-unique', '/tmp/cpra-test-unique')
        self.assertEqual(len(config['cases']), 3)
        for case in config['cases']:
            self.assertEqual(case['evidence_type'], 'local_integration')
            self.assertEqual(case['observation_boundary'], 'effect')
            self.assertTrue(case['observer'])
        monitors = config['manifest']['monitors']
        kube = monitors[1]['intervention']['target']
        self.assertEqual(kube['namespace'], 'cpra-test-unique')
        self.assertEqual(kube['name'], 'cpra-test-unique')
        self.assertEqual(monitors[2]['intervention']['target']['unit'], 'cpra-test-unique.service')
        pod = cluster_local.deployment('cpra-test-unique')['spec']['template']['spec']
        self.assertEqual(pod['containers'][0]['imagePullPolicy'], 'Never')

    def test_kubernetes_observer_rejects_old_available_pod_during_rollout(self):
        row = {'metadata': {'generation': 2, 'uid': 'fixture'}, 'spec': {'replicas': 1},
               'status': {'observedGeneration': 2, 'replicas': 2, 'updatedReplicas': 1,
                          'readyReplicas': 1, 'availableReplicas': 1}}
        self.assertFalse(observe_effect.kubernetes_state(row)['ready'])
        row['status']['replicas'] = 1
        self.assertTrue(observe_effect.kubernetes_state(row)['ready'])
        row['status']['observedGeneration'] = 1
        self.assertFalse(observe_effect.kubernetes_state(row)['ready'])

    def test_icmp_requires_both_request_and_reply_in_same_run(self):
        counts = observe_icmp.read_counts('Ip: ignored\nIcmp: InMsgs InEchos InEchoReps\nIcmp: 10 3 4\n')
        self.assertEqual(counts, {'InEchos': 3, 'InEchoReps': 4})
        before = dict(counts, run_id='one')
        self.assertFalse(observe_icmp.result(before, counts, 'one')['observed'])
        self.assertFalse(observe_icmp.result(before, {'InEchos': 4, 'InEchoReps': 4}, 'one')['observed'])
        after = {'InEchos': 4, 'InEchoReps': 5}
        self.assertTrue(observe_icmp.result(before, after, 'one')['observed'])
        self.assertFalse(observe_icmp.result(before, after, 'two')['observed'])
        with self.assertRaises(ValueError):
            observe_icmp.read_counts('Icmp: InEchos\n')


if __name__ == '__main__':
    unittest.main()
