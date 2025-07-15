#!/usr/bin/python3

# Read all xml files in the current directory, parse them, and print the results as a csv

import os
import sys

import xml.etree.ElementTree as ET

def parse_xml_file(file_path, image, machine_type, test_name):
    """Parse an XML file and return a list of dictionaries with the parsed data."""
    try:
        tree = ET.parse(file_path)
    except ET.ParseError as e:
        print(f'Error parsing {file_path}: {e}', file=sys.stderr)
        return {}
    root = tree.getroot()

    data = []
    # Iterate through each 'testcase' element and extract relevant information
    for testcase in root.findall('.//testcase'):
        test_data = {
            'image': image,
            'machine_type': machine_type,
            'class': test_name,
            'name': testcase.get('name', ''),
            'time': testcase.get('time', '0'),
            'status': 'passed',  # Default status
            'message': '',       # Default message
        }

        # Check for failure elements
        failure = testcase.find('failure')
        if failure is not None:
            test_data['status'] = 'failed'
            test_data['message'] = failure.text.strip() if failure.text else ''

        # Check for skipped elements
        skipped = testcase.find('skipped')
        if skipped is not None:
            test_data['status'] = 'skipped'
            test_data['message'] = skipped.text.strip() if skipped.text else ''

        data.append(test_data)
    return data

def main():
    """Main function to parse all XML files in the current directory."""
    all_data = []
    for file_name in os.listdir(os.getcwd()):
        if file_name.endswith('.xml'):
            file_path = os.path.join('.', file_name)
            # Extract image, machine type, and test name from the file name (excluding .xml extension)
            (image, machine_type, test_name) = file_name[:-4].split('_')
            print(f'Parsing {file_path}...', file=sys.stderr)
            all_data.extend(parse_xml_file(file_path, image, machine_type, test_name))

    # Print the results as CSV
    print('image,machine_type,class,name,status,time,message', file=sys.stderr)
    for data in all_data:
        if data == {}:
            continue
        print(f"{data['image']},{data['machine_type']},{data['class']},{data['name']},{data['status'].replace("\n", "\\n").replace(",", ":")},{data['time']},{data['message'].replace("\n", "\\n").replace(",", ":")}")

if __name__ == '__main__':
    main()
