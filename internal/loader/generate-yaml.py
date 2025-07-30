
import yaml
import copy

class NoAliasDumper(yaml.Dumper):
    def ignore_aliases(self, data):
        return True

with open('/home/ziad/work/cpra/cpra-git/cpra/internal/loader/test.yaml', 'r') as f:
    data = yaml.safe_load(f)

existing_monitors = data.get('monitors', [])
if not existing_monitors:
    print("No monitors found in test.yaml")
    exit()

total_monitors_required = 10000
monitors_to_add = total_monitors_required - len(existing_monitors)

all_monitors = copy.deepcopy(existing_monitors)

for i in range(monitors_to_add):
    template_monitor = copy.deepcopy(existing_monitors[i % len(existing_monitors)])
    template_monitor['name'] = f'Monitor-{len(existing_monitors) + i + 1}'
    all_monitors.append(template_monitor)

data['monitors'] = all_monitors

with open('/home/ziad/work/cpra/cpra-git/cpra/internal/loader/test-small.yaml', 'w') as f:
    yaml.dump(data, f, Dumper=NoAliasDumper, sort_keys=False)

print(f"Successfully generated {len(all_monitors)} monitors in test-medium.yaml")
