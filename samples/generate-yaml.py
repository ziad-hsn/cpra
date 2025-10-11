
import yaml
import copy
import sys

class NoAliasDumper(yaml.Dumper):
    def ignore_aliases(self, data):
        return True

# Get arguments
if len(sys.argv) != 3:
    print("Usage: python generate-yaml.py <monitor_count> <output_file>")
    sys.exit(1)

total_monitors_required = int(sys.argv[1])
output_file = sys.argv[2]

with open('/home/ziad/Desktop/work/ark-migration/cpra/internal/loader/test.yaml', 'r') as f:
    data = yaml.safe_load(f)

existing_monitors = data.get('monitors', [])
if not existing_monitors:
    print("No monitors found in test.yaml")
    exit()

monitors_to_add = total_monitors_required - len(existing_monitors)

all_monitors = copy.deepcopy(existing_monitors)

for i in range(monitors_to_add):
    template_monitor = copy.deepcopy(existing_monitors[i % len(existing_monitors)])
    template_monitor['name'] = f'Monitor-{len(existing_monitors) + i + 1}'
    all_monitors.append(template_monitor)

data['monitors'] = all_monitors

with open(output_file, 'w') as f:
    yaml.dump(data, f, Dumper=NoAliasDumper, sort_keys=False)

print(f"Successfully generated {len(all_monitors)} monitors in {output_file}")
