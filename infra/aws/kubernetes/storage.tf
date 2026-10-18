# The cluster's default StorageClass. EBS encryption-by-default is off in the
# account and must not be changed account-wide, so encryption is requested
# here: health data (backups, NATS, Postgres until Supabase) is encrypted at
# rest. Retain keeps the volume when a PVC is deleted; clean up by hand.
resource "kubernetes_storage_class_v1" "gp3_encrypted" {
  metadata {
    name = "gp3-encrypted"
    annotations = {
      "storageclass.kubernetes.io/is-default-class" = "true"
    }
  }

  storage_provisioner    = "ebs.csi.aws.com"
  reclaim_policy         = "Retain"
  volume_binding_mode    = "WaitForFirstConsumer"
  allow_volume_expansion = true

  parameters = {
    type                        = "gp3"
    encrypted                   = "true"
    "csi.storage.k8s.io/fstype" = "ext4"
  }
}
