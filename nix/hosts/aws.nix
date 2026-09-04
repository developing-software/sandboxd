# Booted from the official NixOS AMI: the amazon-image profile brings the root/ESP
# filesystems, growpart and root's ssh key from instance metadata. ec2.efi already
# follows the architecture.
{ modulesPath, ... }:
{
  imports = [ (modulesPath + "/virtualisation/amazon-image.nix") ];
}
