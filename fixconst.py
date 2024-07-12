import os

directory_path = 'sys/freebsd'

fixed_cnt = 0
for filename in os.listdir(directory_path):
    if filename.endswith(".txt.const"):
        filepath = os.path.join(directory_path, filename)
        file = open(filepath, 'r')
        processed = []
        should_modify = True
        for i, line in enumerate(file):
            if i == 0:
                # This line is the do not edit line
                processed.append(line)
            elif i == 1:
                if '386' in line:
                    # We already ran our script or consts were generated correctly
                    should_modify = False
                    break
                processed.append(line.rstrip() + ', 386, amd64, riscv64\n')
            else:
                parts = line.split('=')
                if len(parts) == 2:
                    left_part = parts[0].strip()
                    right_part = parts[1].split(':')[1].strip() if ':' in parts[1] else parts[1].strip()
                    processed.append(f"{left_part} = {right_part}\n")
        
        file.close()
        if should_modify:
            fixed_cnt += 1
            file = open(filepath, 'w')
            file.writelines(processed)
            file.close()

if fixed_cnt != 0:
    print(f'Fixed {fixed_cnt} files.')
else:
    print('Fixed 0 files. Either const files are missing or they are good to go already.')
