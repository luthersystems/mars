alb_ingress_controller
=========

Removes a helm release of the legacy aws-alb-ingress-controller v1.

The role used to install that controller. v1 watches Ingress in
`extensions/v1beta1`, which Kubernetes 1.22 removed, so it cannot work on any
EKS version AWS supports. Use the `aws_lb_controller` role to install the
AWS Load Balancer Controller. Including this role with its default tasks now
fails with that message (mars#322).

To remove the old release and its copied chart from a cluster:

```yaml
- ansible.builtin.include_role:
    name: alb_ingress_controller
    tasks_from: uninstall
```
