
import yaml
import sys
import copy

def replicate_monitors(input_file, output_file, n):
    """
    Reads monitors from an input YAML file, replicates them n times with unique names,
    and writes them to an output YAML file.
    """
    try:
        with open(input_file, 'r') as f:
            data = yaml.safe_load(f)
    except FileNotFoundError:
        print(f"Error: Input file '{input_file}' not found.")
        return
    except yaml.YAMLError as e:
        print(f"Error parsing YAML file: {e}")
        return

    if 'monitors' not in data or not isinstance(data['monitors'], list):
        print("Error: 'monitors' key not found or is not a list in the input file.")
        return

    original_monitors = data['monitors']
    replicated_monitors = []

    for i in range(1, n + 1):
        for monitor in original_monitors:
            new_monitor = copy.deepcopy(monitor)
            original_name = new_monitor.get('name', 'Unnamed Monitor')
            new_monitor['name'] = f"{original_name} - Copy {i}"
            replicated_monitors.append(new_monitor)

    new_data = {'monitors': replicated_monitors}

    try:
        with open(output_file, 'w') as f:
            yaml.dump(new_data, f, default_flow_style=False, sort_keys=False)
        print(f"Successfully replicated {len(original_monitors)} monitors {n} times.")
        print(f"Generated {len(replicated_monitors)} total monitors in '{output_file}'.")
    except IOError as e:
        print(f"Error writing to output file: {e}")

if __name__ == "__main__":
    if len(sys.argv) != 2:
        print("Usage: python3 replicate_monitors.py <number_of_replications>")
    else:
        try:
            replications = int(sys.argv[1])
            if replications <= 0:
                print("Error: Number of replications must be a positive integer.")
            else:
                replicate_monitors('test.yaml', 'replicated_test.yaml', replications)
        except ValueError:
            print("Error: Invalid number. Please provide an integer.")

