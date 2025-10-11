#!/usr/bin/env python3
import argparse
import sys
import yaml
import requests
from itertools import cycle

def load_yaml_file(filepath):
    """Load YAML file and return its contents"""
    try:
        with open(filepath, 'r') as file:
            return yaml.safe_load(file)
    except FileNotFoundError:
        print(f"Error: YAML file not found at {filepath}", file=sys.stderr)
        sys.exit(1)
    except yaml.YAMLError as e:
        print(f"Error parsing YAML file {filepath}: {e}", file=sys.stderr)
        sys.exit(1)

def load_endpoints_from_file(filepath):
    """Load endpoints from a text file and return as a list"""
    try:
        with open(filepath, 'r') as file:
            return [line.strip() for line in file if line.strip()]
    except FileNotFoundError:
        print(f"Error: Endpoints file not found at {filepath}", file=sys.stderr)
        sys.exit(1)

def load_endpoints_from_url(url):
    """Load endpoints from a URL and return as a list"""
    try:
        response = requests.get(url)
        response.raise_for_status()  # Raise an exception for bad status codes
        return [line.strip() for line in response.text.splitlines() if line.strip()]
    except requests.exceptions.RequestException as e:
        print(f"Error fetching endpoints from {url}: {e}", file=sys.stderr)
        sys.exit(1)

def find_urls_in_config(config):
    """Find all URLs in the config dict recursively"""
    urls = []

    def traverse(obj):
        if isinstance(obj, dict):
            for key, value in obj.items():
                if key in ['url', 'host'] and isinstance(value, str):
                    urls.append(value)
                else:
                    traverse(value)
        elif isinstance(obj, list):
            for item in obj:
                traverse(item)

    traverse(config)
    return urls

def replace_urls_in_config(config, old_urls, new_urls_cycle):
    """Replace URLs in config with new ones using a cycle of new URLs"""
    url_mapping = {url: next(new_urls_cycle) for url in old_urls}

    def replace_recursive(obj):
        if isinstance(obj, dict):
            new_dict = {}
            for key, value in obj.items():
                if key in ['url', 'host'] and isinstance(value, str) and value in url_mapping:
                    new_dict[key] = url_mapping[value]
                else:
                    new_dict[key] = replace_recursive(value)
            return new_dict
        elif isinstance(obj, list):
            return [replace_recursive(item) for item in obj]
        else:
            return obj

    return replace_recursive(config)

def save_yaml_file(data, filepath):
    """Save data to YAML file"""
    try:
        with open(filepath, 'w') as file:
            yaml.dump(data, file, default_flow_style=False, indent=2)
    except IOError as e:
        print(f"Error writing to output file {filepath}: {e}", file=sys.stderr)
        sys.exit(1)

def main():
    parser = argparse.ArgumentParser(description='Replaces URLs/hosts in a YAML file with a list of endpoints.')
    parser.add_argument('yaml_input', help='Path to the input YAML file.')
    parser.add_argument('yaml_output', help='Path to save the modified YAML file.')

    group = parser.add_mutually_exclusive_group(required=True)
    group.add_argument('--endpoints-file', help='Path to the text file containing the new endpoints.')
    group.add_argument('--endpoints-url', help='URL to fetch the new endpoints from.')

    args = parser.parse_args()

    # Load endpoints
    if args.endpoints_file:
        print("Loading endpoints from file...")
        endpoints = load_endpoints_from_file(args.endpoints_file)
    else:
        print(f"Loading endpoints from URL: {args.endpoints_url}")
        endpoints = load_endpoints_from_url(args.endpoints_url)

    if not endpoints:
        print("Error: No endpoints found.", file=sys.stderr)
        sys.exit(1)

    # Load YAML file
    print("Loading YAML file...")
    yaml_data = load_yaml_file(args.yaml_input)

    # Find all URLs/hosts in the YAML
    print("Finding URLs to replace...")
    existing_urls = find_urls_in_config(yaml_data)
    print(f"Found {len(existing_urls)} URLs/hosts to replace")

    if not existing_urls:
        print("No URLs/hosts found to replace. Exiting.")
        return

    # Create a cycle of the new endpoints
    endpoint_cycle = cycle(endpoints)

    print("Replacing URLs...")
    modified_data = replace_urls_in_config(yaml_data, existing_urls, endpoint_cycle)

    # Save modified YAML
    print(f"Saving modified YAML to {args.yaml_output}...")
    save_yaml_file(modified_data, args.yaml_output)

    print("Done!")

if __name__ == "__main__":
    main()