# kubectl-watch

Watch for pod events in a namespace

## Build
```sh
git clone https://github.com/benbaker76/kubectl-watch.git
cd kubectl-watch
go mod tidy
go build
```

## Install
```sh
sudo cp kubectl-watch /usr/bin
```

## Usage

```sh
kubectl watch [TYPE] [NAME] [flags] [options]
```

## Examples

**watch for pod events in all namespaces**

`kubectl watch pods`

**watch for the pod DELETED event in the default namespace**

`kubectl watch pods --namespace=default --event=DELETED`

**watch for the pod CHANGED event and when the pod status is Running**

`kubectl watch pods --event=CHANGED --status=Running`

**watch for the pod CHANGED event and when the pod status is Running**

`kubectl watch pod coredns-7cccd78cb7-p6mkn --event=CHANGED --status=Running`

## Flags
```
--event string                   Name of the event to watch. Options are 'ADDED', 'MODIFIED', 'DELETED' or 'BOOKMARK'
--status string                  Name of the status to watch. Options are 'Pending', 'Running', 'Succeeded' 'Failed' or 'Unknown'
```
