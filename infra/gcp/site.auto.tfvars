# The site's DNS records (site.tf, runbook step 10). Committed on purpose: filling these in is a
# reviewed PR, then apply 3. HCP Terraform loads *.auto.tfvars in remote runs. Never set these as
# workspace variables: this file must stay the only place. Empty adds nothing.
#
# Copy the values from Firebase console -> Hosting -> Add custom domain, exactly as the wizard shows
# them ("Quick setup"). The apex: its A record(s) and its TXT. www: either its A record(s) and TXT, or
# its CNAME (then leave site_www_a and site_www_txt empty). Anything else the wizard asks for (an AAAA,
# an _acme-challenge TXT): stop, it isn't modelled here.

# ryftlab.de
site_apex_a   = []
site_apex_txt = ""

# www.ryftlab.de
site_www_a     = []
site_www_txt   = ""
site_www_cname = ""
