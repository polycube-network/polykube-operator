package node

import (
	"context"
	"errors"
	"fmt"
	"github.com/ekoops/polykube-operator/pkg/env"
	"github.com/ekoops/polykube-operator/pkg/types"
	"github.com/vishvananda/netlink"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"net"
	ctrl "sigs.k8s.io/controller-runtime"
)

var (
	// logger used throughout the package
	log  = ctrl.Log.WithName("node-pkg")
	Conf *Configuration
)

// Get returns a node object describing the cluster node corresponding to the provided name.
// The request is performed using directly the provided cset (without using caching mechanism from
// the controller-runtime library
func Get(cset *kubernetes.Clientset, name string) (*v1.Node, error) {
	l := log.WithValues("node", name)
	n, err := cset.CoreV1().Nodes().Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		l.Error(err, "failed to retrieve cluster node info")
		return nil, fmt.Errorf("failed to retrieve cluster node info")
	}
	l.V(1).Info("cluster node info retrieved")
	return n, nil
}

// GetExtIface returns info about the external interface of the provided node. The external interface is
// found using the first parsable address of type NodeInternalIP inside the provided node object.
func GetExtIface(n *v1.Node) (*types.Iface, error) {
	l := log.WithValues("node", n.Name)
	// extracting ip of the node external interface
	extIfaceIP := GetIP(n)
	if extIfaceIP == nil {
		l.Error(
			errors.New("no NodeInternalIP found inside node object"),
			"failed to extract the IP of the cluster node external interface",
		)
		return nil, errors.New("failed to extract the IP of the cluster node external interface")
	}

	// retrieving the list of all the interfaces in the node
	links, err := netlink.LinkList()
	if err != nil {
		l.Error(err, "failed to retrieve the list of all the cluster node interfaces")
		return nil, errors.New("failed to retrieve the list of all the cluster node interfaces")
	}

	// searching for the interface whose ip list contains the external interface ip
	for _, link := range links {
		linkName := link.Attrs().Name
		linkLog := l.WithValues("interface", linkName)
		addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
		if err != nil {
			linkLog.Error(err, "failed to retrieve the list of the node interface addresses")
			return nil, errors.New("failed to retrieve the list of the node interface addresses")
		}
		// scanning the list of addresses of the current interface in order to determine if the list contains
		// the external interface one
		for _, addr := range addrs {
			if addr.IP.Equal(extIfaceIP) {
				// interface found
				extIface := &types.Iface{
					IPNet: addr.IPNet,
					Link:  link,
				}
				linkLog.WithValues(
					"info", fmt.Sprintf("%+v", extIface),
				).V(1).Info("obtained cluster node external interface info")
				return extIface, nil
			}
		}
	}
	l.Error(
		errors.New("no interface for the retrieved interface external IP"),
		"failed to retrieve cluster node external interface info", "IP", extIfaceIP)
	return nil, errors.New("failed to retrieve cluster node external interface info")
}

// GetDefaultGatewayIPNet returns the IP address and prefix length of the default gateway for the cluster node
// external interface
func GetDefaultGatewayIPNet(extIface *types.Iface) (*net.IPNet, error) {
	extIfaceName := extIface.Link.Attrs().Name
	l := log.WithValues("interface", extIfaceName)

	// retrieving the default route by performing a query to an (hopefully) external IP address
	// TODO temporary solution
	routes, err := netlink.RouteGet(net.IPv4(1, 0, 0, 0))
	if err != nil {
		log.Error(err, "failed to retrieve the default route through the cluster node external interface")
		return nil, errors.New("failed to retrieve the default route through the cluster node external interface")
	}
	if len(routes) != 1 {
		log.Error(
			errors.New("multiple default route"),
			"failed to determine a single default route for the cluster node",
		)
		return nil, errors.New("failed to determine a single default route for the cluster node")
	}
	route := routes[0]

	// checking that the route link index is equal to the external interface index
	routeLI := route.LinkIndex
	extIfaceLI := extIface.Link.Attrs().Index
	if routeLI != extIfaceLI {
		l.V(1).Error(
			errors.New("the route link index doesn't match the external interface link index"),
			"link index mismatch",
			"routeLinkIndex", routeLI,
			"extIfaceLinkIndex", extIfaceLI,
		)
		return nil, errors.New("the route link index doesn't match the external interface link index")
	}

	gwIPNet := &net.IPNet{
		IP:   route.Gw,
		Mask: extIface.IPNet.Mask, // using the same prefix length of the external interface IP address
	}

	log.V(1).Info(
		"retrieved the IP address and prefix length of the default gateway for the cluster node external interface",
		"IP", fmt.Sprintf("%+v", gwIPNet),
	)
	return gwIPNet, nil
}

// GetDefaultGatewayMAC returns the MAC of the default gateway for the cluster node external interface
func GetDefaultGatewayMAC(extIface *types.Iface, gwIP net.IP) (net.HardwareAddr, error) {
	extIfaceName := extIface.Link.Attrs().Name
	extIfaceLI := extIface.Link.Attrs().Index
	l := log.WithValues("interface", extIfaceName)
	// retrieving the neighbor list of the external interface
	neighs, err := netlink.NeighList(extIfaceLI, netlink.FAMILY_V4)
	if err != nil {
		l.Error(err, "failed to retrieve the external interface neighbor list")
		return nil, errors.New("failed to retrieve the external interface neighbor list")
	}
	// searching for a neighbor whose IP address is the default gateway one
	for _, neigh := range neighs {
		if neigh.IP.Equal(gwIP) {
			gwMAC := neigh.HardwareAddr
			l.V(1).Info("retrieved the MAC of the default gateway for the cluster node external interface", "MAC", gwMAC)
			return gwMAC, nil
		}
	}
	l.Error(
		errors.New("no ARP entry for default gateway"),
		"failed to retrieve the MAC of the default gateway for the cluster node external interface",
	)
	return nil, errors.New("failed to retrieve the MAC of the default gateway for the cluster node external interface")
}

// GetIP extract the first parsable address of type NodeInternalIP inside the provided node object
func GetIP(n *v1.Node) net.IP {
	l := log.WithValues("node", n.Name)
	for _, addr := range n.Status.Addresses {
		if addr.Type == v1.NodeInternalIP {
			nodeIP := net.ParseIP(addr.Address)
			if nodeIP == nil {
				l.Error(errors.New("failed to parse"), "skipped node IP", "IP", addr.Address)
			}
			l.V(1).Info("obtained node IP", "IP", nodeIP)
			return nodeIP
		}
	}
	l.Error(errors.New("not found"), "failed to obtain node IP")
	return nil
}

// IsReady returns true if the provided node is ready; false otherwise
func IsReady(n *v1.Node) bool {
	l := log.WithValues("node", n.Name)
	for _, c := range n.Status.Conditions {
		if c.Type == v1.NodeReady && c.Status == v1.ConditionTrue {
			l.V(1).Info("the node is ready")
			return true
		}
	}
	l.V(1).Info("the node is not ready")
	return false
}

func LoadConfig() error {
	// creating the in-cluster config
	clusterConfig, err := rest.InClusterConfig()
	if err != nil {
		return err
	}

	// creating the clientset
	cset, err := kubernetes.NewForConfig(clusterConfig)
	if err != nil {
		return err
	}

	name := env.Conf.NodeName

	node, err := Get(cset, name)
	if err != nil {
		return err
	}
	podCIDR, err := ParsePodCIDR(node)
	if err != nil {
		return err
	}
	podGwIPNet, err := CalcPodDefaultGatewayIPNet(podCIDR)
	if err != nil {
		return err
	}
	// the following GwInfo struct has to be completed by adding the Mac address:
	// once the polycube infrastructure is ready, a call to LoadNodePodDefaultGatewayMAC has to be performed
	podGwInfo := &types.GwInfo{
		IPNet: podGwIPNet,
	}

	extIface, err := GetExtIface(node)
	if err != nil {
		return err
	}

	nodeVtepIPNet, err := CalcVtepIPNet(node)
	if err != nil {
		return err
	}

	nodeGwIPNet, err := GetDefaultGatewayIPNet(extIface)
	if err != nil {
		return err
	}
	nodeGwMAC, err := GetDefaultGatewayMAC(extIface, nodeGwIPNet.IP)
	if err != nil {
		return err
	}
	nodeGwInfo := &types.GwInfo{
		IPNet: nodeGwIPNet,
		MAC:   nodeGwMAC,
	}

	Conf = &Configuration{
		Node:          node,
		PodCIDR:       podCIDR,
		PodGwInfo:     podGwInfo,
		ExtIface:      extIface,
		NodeVtepIPNet: nodeVtepIPNet,
		NodeGwInfo:    nodeGwInfo,
	}

	return nil
}
