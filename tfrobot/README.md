# tfrobot

<a href='https://github.com/jpoles1/gopherbadger' target='_blank'>![gopherbadger-tag-do-not-edit](https://img.shields.io/badge/Go%20Coverage-88%25-brightgreen.svg?longCache=true&style=flat)</a>

tfrobot is tool designed to automate mass deployment of groups of VMs on ThreeFold Grid, with support of multiple retries for failed deployments

## Features

- **Mass Deployment:** Deploy groups of vms on ThreeFold Grid simultaneously.
- **Mass Cancellation:** Cancel all vms on ThreeFold Grid defined in configuration file simultaneously.
- **Load Deployments:** Load gorups of vms deployed using TFRobot simultaneously.
- **Customizable Configurations:** Define Node groups, VMs groups and other configurations through YAML or JSON file.

## Download

1.  Download the binaries from [releases](https://github.com/threefoldtech/tfgrid-sdk-go/releases)
2.  Extract the downloaded files
3.  Move the binary to any of `$PATH` directories, for example:

```bash
mv tfrobot /usr/local/bin
```

4.  Create a new configuration file, for example `config.yaml`:

```yaml
node_groups:

  - name: group_a
    nodes_count: 2 # amount of nodes to be found
    free_cpu: 2 # number of logical cores
    free_mru: 32 # amount of memory in GB
    free_ssd: 20 # amount of ssd storage in GB
    free_hdd: 5 # amount of hdd storage in GB
    dedicated: false # are nodes dedicated
    public_ip4: false # should the nodes have free ip v4
    public_ip6: false # should the nodes have free ip v6
    certified: false # should the nodes be certified(if false the nodes could be certified of diy) 
    region: europe # region could be the name of the continents the nodes are located in (africa, americas, antarctic, antarctic ocean, asia, europe, oceania, polar)
vms:
  - name: example1
    vms_count: 1 # amount of vms with the same configurations
    node_group: group_a # the name of the predefined group of nodes
    cpu: 2 # number of logical cores
    mem: 2 # amount of  memory in GB
    public_ip4: false
    public_ip6: false
    ygg_ip: false
    mycelium_ip: true
    flist: "https://hub.grid.tf/tf-official-apps/threefoldtech-ubuntu-22.04.flist"
    entry_point: '/sbin/zinit init'
    root_size: 0 # root size in GB, 0 is the default
    ssh_key: my_key # the name of the predefined ssh key, will be defined below
    env_vars:
      key1: val1


ssh_keys: # map of ssh keys with key=name and value=the actual ssh key
  my_key: "example-key"

mnemonic: REPLACE WITH YOUR MNEMONIC # mnemonic of the user
network: main # eg: main, test, qa, dev

```

You can use this [example](./example/conf.yaml) for further guidance

>**Please** make sure to replace placeholders and adapt the groups based on your actual project details.

>**Notes:**
>> The VMs may utilize a different number of nodes than requested due
to the retries filtering out additional nodes in case of failure.
Consequently, it's possible to utilize more nodes than initially requested.
>> Duplicate field values are ignored; only last occurrence will be considered.

5.  Run the deployer with path to the config file

```bash
tfrobot deploy -c path/to/your/config.yaml
```

## Supported Configurations

### Config File

| Field | Description| Supported Values|
| :---:   | :---: | :---: |
| [node_group](#node-group) | description of all resources needed for each node_group | list of structs of type node_group |
| [vms](#vms-groups) | description of resources needed for deploying groups of vms belong to node_group | list of structs of type vms |
| ssh_keys | map of ssh keys with key=name and value=the actual ssh key | map of string to string |
| mnemonic | mnemonic of the user | should be valid mnemonic |
| network | valid network of ThreeFold Grid networks | main, test, qa, dev |
| max_retries | times of retries of failed node groups | positive integer |

### Node Group

| Field | Description| Supported Values|
| :---:   | :---: | :---: |
| name | name of node_group | node group name should be unique |
| nodes_count | number of nodes in node group| nonzero positive integer |
| free_cpu | number of cpu of node | nonzero positive integer max = 32 |
| free_mru | free memory in the node in GB | min = 0.25, max = 256 |
| free_ssd | free ssd storage in the node in GB | positive integer value |
| free_hdd | free hdd storage in the node in GB | positive integer value |
| dedicated | are nodes dedicated | `true` or `false` |
| public_ip4 | should the nodes have free ip v4 | `true` or `false` |
| public_ip6 | should the nodes have free ip v6 | `true` or `false` |
| certified | should the nodes be certified(if false the nodes could be certified or DIY)  | `true` or `false` |
| region | region could be the name of the continents the nodes are located in | africa, americas, antarctic, antarctic ocean, asia, europe, oceania, polar |

### Vms Groups

| Field | Description| Supported Values|
| :---:   | :---: | :---: |
| name | name of vm group | string value with no special characters |
| vms_count | number of vms in vm group| nonzero positive integer |
| node_group | name of node_group the vm belongs to | should be defined in node_groups |
| cpu | number of cpu for vm | nonzero positive integer max = 32  |
| mem | free memory in the vm in GB | min = 0.25, max 256 |
| ygg_ip | should the vm have yggdrasil ip | `true` or `false` |
| mycelium_ip | should the vm have mycelium ip | `true` or `false` |
| public_ip4 | should the vm have free ip v4 | `true` or `false` |
| public_ip6 | should the vm have free ip v6 | `true` or `false` |
| flist | should be a link to valid flist | valid flist url with `.flist` or `.fl` extension |
| entry_point | entry point of the flist | path to the entry point in the flist |
| ssh_key | key of ssh key defined in the ssh_keys map | should be valid ssh_key defined in the ssh_keys map |
| env_vars | map of env vars | map of type string to string |
| ssd | list of disks | should be of type disk|
| root_size | root size in GB | 0 for default root size, max 10TB |

### Disk

| Field | Description| Supported Values|
| :---:   | :---: | :---: |
| size | disk size in GB| positive integer min = 15 |
| mount_point | disk mount point | path to mountpoint |

> **Notes:**
>> All storage resources are expected to be in GB.

>> In case of YAML input, floating point portion of int values will be ignored.

>> Ensure that memory precision does not exceed 0.001,
any value greater than this threshold will be disregarded.

## Usage

### Subcommands

- **deploy:** used to mass deploy groups of vms with specific configurations

```bash
tfrobot deploy -c path/to/your/config.yaml
```

- **cancel:** used to cancel all vms deployed using specific configurations

```bash
tfrobot cancel -c path/to/your/config.yaml
```

- **load:** used to load all vms deployed using specific configurations

```bash
tfrobot load -c path/to/your/config.yaml
```

### Flags

| Flag | Usage |
| :---:   | :---: |
| -c | used to specify path to configuration file |
| -o | used to specify path to output file to store the output info in |
| -d | allow debug logs to appear in the output logs |
| -h | help |

> **Notes:**
>> Parsing is based on file extension, json format if the file had json
extension, yaml format otherwise

>> Make sure to use every flag once. If the flag is repeated,
it will ignore all values and take the last value of the flag.

## Using Docker

```bash
docker build -t tfrobot -f Dockerfile ../
docker run -v $(pwd)/config.yaml:/config.yaml -it tfrobot:latest deploy -c /config.yaml
```

## Build

To build the deployer locally clone the repo and run the following command inside the repo directory:

```bash
make build
```

## Test

To run the deployer tests run the following command inside the repo directory:

```bash
make test
```

## Logs

To ensure a complete log history, append `2>&1 | tee path/to/log/file`
to the command being executed.

```bash
tfrobot deploy -c path/to/your/config.yaml 2>&1 | tee path/to/log/file
```
