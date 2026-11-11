# Hostinger image services on the managed host. Nix owns the network; cloud-init
# keeps only the datasource, with no account, SSH, script or network changes.
{ lib, ... }:
{
  imports = [ ./hostinger-services.nix ];

  boot.kernelParams = [ "earlyprintk=ttyS0,115200" "consoleblank=0" "memhp_default_state=online" ];

  services.cloud-init = {
    network.enable = false;
    settings = {
      network.config = lib.mkDefault "disabled";
      preserve_hostname = true;
      allow_userdata = false;
      vendor_data.enabled = false;
      vendor_data2.enabled = false;
      cloud_init_modules = [ ];
      cloud_config_modules = [ ];
      cloud_final_modules = [ ];
    };
  };
}
